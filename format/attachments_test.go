package format

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// These tests pin the cross-format conversion of file attachments:
// OpenAI file/input_audio/video_url content parts ↔ Anthropic
// document/audio/video content blocks. Same-format relays pass the body
// through untouched, so attachments only need explicit handling here.

// oaiToAnthUserBlocks converts an OpenAI request body to Anthropic and returns
// the content blocks of the first user message.
func oaiToAnthUserBlocks(t *testing.T, body string) []map[string]interface{} {
	t.Helper()
	anthReq, err := OpenAIRequestToAnthropic([]byte(body), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	return anthUserBlocks(t, anthReq)
}

func anthUserBlocks(t *testing.T, anthReq []byte) []map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.Unmarshal(anthReq, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	msgs, ok := result["messages"].([]interface{})
	if !ok || len(msgs) == 0 {
		t.Fatalf("no messages in converted request")
	}
	userMsg, ok := msgs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first message is not an object")
	}
	content, ok := userMsg["content"].([]interface{})
	if !ok {
		t.Fatalf("user message content is not an array")
	}
	blocks := make([]map[string]interface{}, 0, len(content))
	for _, c := range content {
		blocks = append(blocks, c.(map[string]interface{}))
	}
	return blocks
}

func anthToOaiUserParts(t *testing.T, body string) []map[string]interface{} {
	t.Helper()
	oaiReq, err := AnthropicRequestToOpenAI([]byte(body), "gpt-4o")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(oaiReq, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	msgs, ok := result["messages"].([]interface{})
	if !ok || len(msgs) == 0 {
		t.Fatalf("no messages in converted request")
	}
	// The first user message may be preceded by a system message.
	var userMsg map[string]interface{}
	for _, m := range msgs {
		if mm, ok := m.(map[string]interface{}); ok && mm["role"] == "user" {
			userMsg = mm
			break
		}
	}
	if userMsg == nil {
		t.Fatalf("no user message in converted request")
	}
	content, ok := userMsg["content"].([]interface{})
	if !ok {
		t.Fatalf("user message content is not an array")
	}
	parts := make([]map[string]interface{}, 0, len(content))
	for _, c := range content {
		parts = append(parts, c.(map[string]interface{}))
	}
	return parts
}

func oaiUserMsg(parts string) string {
	return `{"model":"gpt-4o","messages":[{"role":"user","content":[` + parts + `]}]}`
}

func anthUserMsg(blocks string) string {
	return `{"model":"claude-sonnet-4","messages":[{"role":"user","content":[` + blocks + `]}]}`
}

// wrapBase64 MIME-wraps a base64 string at the given column width.
func wrapBase64(b64 string, width int) string {
	var sb strings.Builder
	for i := 0; i < len(b64); i += width {
		end := i + width
		if end > len(b64) {
			end = len(b64)
		}
		sb.WriteString(b64[i:end])
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestOpenAIToAnthropic_FileAttachments(t *testing.T) {
	// Regression: OpenAI file content parts were silently dropped, so files
	// never reached Anthropic-compatible upstreams (and text-based documents
	// were lost when routed across formats).
	pdfData := "%PDF-1.4\nfake pdf bytes\n"
	pdfB64 := base64.StdEncoding.EncodeToString([]byte(pdfData))

	t.Run("pdf data uri becomes document base64", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,`+pdfB64+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		doc := blocks[0]
		if doc["type"] != "document" {
			t.Fatalf("block type = %v, want document", doc["type"])
		}
		if doc["title"] != "report.pdf" {
			t.Errorf("document title = %v, want report.pdf", doc["title"])
		}
		source := doc["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != pdfB64 {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("text data uri becomes document text source", func(t *testing.T) {
		// Anthropic rejects base64 document sources with media types other
		// than application/pdf — text files must decode into a text source.
		csvText := "name,age\nalice,30\n"
		csvB64 := base64.StdEncoding.EncodeToString([]byte(csvText))
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"filename":"data.csv","file_data":"data:text/csv;base64,`+csvB64+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "text" {
			t.Fatalf("source type = %v, want text", source["type"])
		}
		if source["media_type"] != "text/plain" || source["data"] != csvText {
			t.Errorf("unexpected text source: %v", source)
		}
	})

	t.Run("url file_data becomes document url source", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"file_data":"https://example.com/report.pdf"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "url" || source["url"] != "https://example.com/report.pdf" {
			t.Errorf("unexpected url source: %v", source)
		}
	})

	t.Run("uppercase http scheme is accepted", func(t *testing.T) {
		// RFC 3986 schemes are case-insensitive; the URL must not fall
		// through to the bare-base64 sniff branch.
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"file_data":"HTTPS://example.com/report.pdf"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "url" || source["url"] != "HTTPS://example.com/report.pdf" {
			t.Errorf("unexpected url source: %v", source)
		}
	})

	t.Run("raw base64 pdf is sniffed from content", func(t *testing.T) {
		// Some SDKs send bare base64 without a data: prefix; the media type
		// must come from sniffing the content, not the filename.
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"filename":"report.pdf","file_data":"`+pdfB64+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != pdfB64 {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("raw base64 text is sniffed from content", func(t *testing.T) {
		text := "The quick brown fox\njumps over the lazy dog\n"
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"filename":"notes.txt","file_data":"`+base64.StdEncoding.EncodeToString([]byte(text))+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "text" || source["media_type"] != "text/plain" || source["data"] != text {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("newline-wrapped bare base64 is sniffed and unwrapped", func(t *testing.T) {
		// MIME-style base64 wraps lines at 76 chars; the sniff prefix must
		// cut on data-char boundaries and the forwarded payload must be
		// unwrapped (upstream base64 decoders expect plain data).
		payload := "%PDF-1.4\n" + strings.Repeat("0123456789abcdef", 512) + "\n%%EOF\n"
		wrapped := wrapBase64(base64.StdEncoding.EncodeToString([]byte(payload)), 76)
		if wrapped == "" {
			t.Fatal("wrapBase64 produced empty string")
		}
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"file_data":"`+strings.ReplaceAll(wrapped, "\n", "\\n")+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "application/pdf" {
			t.Fatalf("unexpected source: %v", source)
		}
		unwrapped := strings.ReplaceAll(wrapped, "\n", "")
		if source["data"] != unwrapped {
			t.Errorf("data contains newlines or differs: got %d chars, want %d", len(source["data"].(string)), len(unwrapped))
		}
	})

	t.Run("uppercase data scheme and media type are accepted", func(t *testing.T) {
		// RFC 2397 scheme and RFC 6838 media types are case-insensitive.
		pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n"))
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"file","file":{"file_data":"DATA:Application/PDF;base64,`+pdfB64+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != pdfB64 {
			t.Errorf("unexpected source: %v", source)
		}
		// Same scheme case-insensitivity must hold for image parts.
		imgBlocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"image_url","image_url":{"url":"DATA:image/png;base64,iVBORw=="}}`))
		if len(imgBlocks) != 1 {
			t.Fatalf("expected 1 image block, got %d", len(imgBlocks))
		}
		imgSource := imgBlocks[0]["source"].(map[string]interface{})
		if imgSource["type"] != "base64" || imgSource["media_type"] != "image/png" || imgSource["data"] != "iVBORw==" {
			t.Errorf("unexpected image source: %v", imgSource)
		}
	})

	t.Run("empty data uri payload is dropped", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"text","text":"read this"},{"type":"file","file":{"file_data":"data:application/pdf;base64,"}}`))
		if len(blocks) != 1 || blocks[0]["type"] != "text" {
			t.Fatalf("expected only the text block, got %v", blocks)
		}
	})

	t.Run("non-utf8 text file is dropped", func(t *testing.T) {
		garbage := base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe, 0x00, 0x80})
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"text","text":"read this"},{"type":"file","file":{"file_data":"data:text/plain;base64,`+garbage+`"}}`))
		if len(blocks) != 1 || blocks[0]["type"] != "text" {
			t.Fatalf("expected only the text block, got %v", blocks)
		}
	})

	t.Run("binary non-pdf file is dropped", func(t *testing.T) {
		// docx/xlsx (zip containers) have no Anthropic document
		// representation — better to drop than to mislabel as PDF.
		docxB64 := base64.StdEncoding.EncodeToString([]byte("PK\x03\x04fake zip content"))
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"text","text":"read this"},{"type":"file","file":{"filename":"doc.docx","file_data":"`+docxB64+`"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected only the text block, got %d", len(blocks))
		}
		if blocks[0]["type"] != "text" {
			t.Errorf("remaining block type = %v, want text", blocks[0]["type"])
		}
	})

	t.Run("file_id only is dropped", func(t *testing.T) {
		// Resolving a file_id would need a Files API round-trip with the
		// client's own account — not possible through the gateway.
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"text","text":"hello"},{"type":"file","file":{"file_id":"file-abc123"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected only the text block, got %d", len(blocks))
		}
		if blocks[0]["type"] != "text" {
			t.Errorf("remaining block type = %v, want text", blocks[0]["type"])
		}
	})
}

func TestOpenAIToAnthropic_AudioAndVideo(t *testing.T) {
	// Regression: input_audio and video_url parts were silently dropped when
	// routing OpenAI clients to Anthropic-format upstreams (MiniMax M3 and
	// other Anthropic-compatible endpoints accept audio/video blocks).
	t.Run("input_audio wav becomes audio block", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"input_audio","input_audio":{"data":"V0FWRQ==","format":"wav"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		audio := blocks[0]
		if audio["type"] != "audio" {
			t.Fatalf("block type = %v, want audio", audio["type"])
		}
		source := audio["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "audio/wav" || source["data"] != "V0FWRQ==" {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("input_audio mp3 becomes audio block", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"input_audio","input_audio":{"data":"SUQzAw==","format":"mp3"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["media_type"] != "audio/mpeg" {
			t.Errorf("media_type = %v, want audio/mpeg", source["media_type"])
		}
	})

	t.Run("video_url becomes video url block", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		video := blocks[0]
		if video["type"] != "video" {
			t.Fatalf("block type = %v, want video", video["type"])
		}
		source := video["source"].(map[string]interface{})
		if source["type"] != "url" || source["url"] != "https://example.com/clip.mp4" {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("video_url data uri becomes video base64 block", func(t *testing.T) {
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAAAIGZ0eXBtcDQy"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "video/mp4" || source["data"] != "AAAAIGZ0eXBtcDQy" {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("input_audio with unknown format passes through as audio codec", func(t *testing.T) {
		// Anthropic-side audio media types are open-ended, so unknown
		// codecs pass through as audio/<codec> instead of being dropped.
		blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
			`{"type":"input_audio","input_audio":{"data":"T2dnUw==","format":"ogg"}}`))
		if len(blocks) != 1 {
			t.Fatalf("expected 1 block, got %d", len(blocks))
		}
		source := blocks[0]["source"].(map[string]interface{})
		if source["media_type"] != "audio/ogg" {
			t.Errorf("media_type = %v, want audio/ogg", source["media_type"])
		}
	})
}

func TestOpenAIToAnthropic_MixedAttachmentMessage(t *testing.T) {
	// Regression: attachments must survive conversion in order alongside
	// text and images instead of being dropped.
	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n"))
	blocks := oaiToAnthUserBlocks(t, oaiUserMsg(
		`{"type":"text","text":"analyze"}`+","+
			`{"type":"file","file":{"file_data":"data:application/pdf;base64,`+pdfB64+`"}}`+","+
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw=="}}`+","+
			`{"type":"input_audio","input_audio":{"data":"V0FWRQ==","format":"wav"}}`+","+
			`{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4"}}`))
	if len(blocks) != 5 {
		t.Fatalf("expected 5 blocks, got %d: %v", len(blocks), blocks)
	}
	wantTypes := []string{"text", "document", "image", "audio", "video"}
	for i, want := range wantTypes {
		if blocks[i]["type"] != want {
			t.Errorf("block %d type = %v, want %v", i, blocks[i]["type"], want)
		}
	}
}

func TestAnthropicToOpenAI_DocumentAttachments(t *testing.T) {
	// Regression: Anthropic document blocks were silently dropped when
	// routing Anthropic clients to OpenAI-format upstreams.
	t.Run("document base64 becomes file part", func(t *testing.T) {
		pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n"))
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"document","title":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"`+pdfB64+`"}}`))
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(parts))
		}
		file := parts[0]["file"].(map[string]interface{})
		if parts[0]["type"] != "file" {
			t.Fatalf("part type = %v, want file", parts[0]["type"])
		}
		if file["filename"] != "report.pdf" {
			t.Errorf("filename = %v, want report.pdf", file["filename"])
		}
		if file["file_data"] != "data:application/pdf;base64,"+pdfB64 {
			t.Errorf("file_data = %v", file["file_data"])
		}
	})

	t.Run("document text source becomes file part", func(t *testing.T) {
		text := "name,age\nalice,30\n"
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"name,age\nalice,30\n"}}`))
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(parts))
		}
		file := parts[0]["file"].(map[string]interface{})
		want := "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte(text))
		if file["file_data"] != want {
			t.Errorf("file_data = %v, want %v", file["file_data"], want)
		}
	})

	t.Run("document url becomes file part with url", func(t *testing.T) {
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"document","source":{"type":"url","url":"https://example.com/report.pdf"}}`))
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(parts))
		}
		file := parts[0]["file"].(map[string]interface{})
		if file["file_data"] != "https://example.com/report.pdf" {
			t.Errorf("file_data = %v", file["file_data"])
		}
	})
}

func TestAnthropicToOpenAI_AudioAndVideo(t *testing.T) {
	// Regression: Anthropic-style audio/video blocks (used by Anthropic-
	// compatible providers) were dropped when routed to OpenAI upstreams.
	t.Run("video url becomes video_url part", func(t *testing.T) {
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"video","source":{"type":"url","url":"https://example.com/clip.mp4"}}`))
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(parts))
		}
		vu := parts[0]["video_url"].(map[string]interface{})
		if parts[0]["type"] != "video_url" || vu["url"] != "https://example.com/clip.mp4" {
			t.Errorf("unexpected part: %v", parts[0])
		}
	})

	t.Run("video base64 becomes video_url data uri", func(t *testing.T) {
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"video","source":{"type":"base64","media_type":"video/mp4","data":"AAAAIGZ0eXBtcDQy"}}`))
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(parts))
		}
		vu := parts[0]["video_url"].(map[string]interface{})
		if vu["url"] != "data:video/mp4;base64,AAAAIGZ0eXBtcDQy" {
			t.Errorf("url = %v", vu["url"])
		}
	})

	t.Run("audio base64 becomes input_audio part", func(t *testing.T) {
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"audio","source":{"type":"base64","media_type":"audio/mpeg","data":"SUQzAw=="}}`))
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(parts))
		}
		ia := parts[0]["input_audio"].(map[string]interface{})
		if parts[0]["type"] != "input_audio" || ia["format"] != "mp3" || ia["data"] != "SUQzAw==" {
			t.Errorf("unexpected part: %v", parts[0])
		}
	})

	t.Run("audio url source is dropped", func(t *testing.T) {
		// OpenAI input_audio requires base64 data — a URL has no equivalent.
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"text","text":"hello"},{"type":"audio","source":{"type":"url","url":"https://example.com/a.mp3"}}`))
		if len(parts) != 1 {
			t.Fatalf("expected only the text part, got %d", len(parts))
		}
		if parts[0]["type"] != "text" {
			t.Errorf("remaining part type = %v, want text", parts[0]["type"])
		}
	})

	t.Run("audio codecs outside the openai input_audio enum are dropped", func(t *testing.T) {
		// OpenAI input_audio accepts only wav/mp3 — ogg/flac/opus/aac are
		// the audio-OUTPUT enum and would 400 the whole request upstream.
		parts := anthToOaiUserParts(t, anthUserMsg(
			`{"type":"text","text":"hello"},{"type":"audio","source":{"type":"base64","media_type":"audio/ogg","data":"T2dnUw=="}}`))
		if len(parts) != 1 {
			t.Fatalf("expected only the text part, got %d", len(parts))
		}
		if parts[0]["type"] != "text" {
			t.Errorf("remaining part type = %v, want text", parts[0]["type"])
		}
	})
}

func TestAnthropicToOpenAI_MixedAttachmentMessage(t *testing.T) {
	// Regression: attachments must survive conversion in order alongside
	// text and images instead of being dropped.
	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n"))
	parts := anthToOaiUserParts(t, anthUserMsg(
		`{"type":"text","text":"analyze"}`+","+
			`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"`+pdfB64+`"}}`+","+
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw=="}}`+","+
			`{"type":"video","source":{"type":"url","url":"https://example.com/clip.mp4"}}`+","+
			`{"type":"audio","source":{"type":"base64","media_type":"audio/wav","data":"V0FWRQ=="}}`))
	if len(parts) != 5 {
		t.Fatalf("expected 5 parts, got %d: %v", len(parts), parts)
	}
	wantTypes := []string{"text", "file", "image_url", "video_url", "input_audio"}
	for i, want := range wantTypes {
		if parts[i]["type"] != want {
			t.Errorf("part %d type = %v, want %v", i, parts[i]["type"], want)
		}
	}
}

func TestAttachmentRoundTrip(t *testing.T) {
	// Regression: a request routed OpenAI → Anthropic → OpenAI (failover
	// across format families) must not lose attachment parts.
	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n"))
	oai := oaiUserMsg(
		`{"type":"file","file":{"file_data":"data:application/pdf;base64,` + pdfB64 + `"}}` +
			`,` + `{"type":"input_audio","input_audio":{"data":"V0FWRQ==","format":"wav"}}` +
			`,` + `{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4"}}`)

	anthReq, err := OpenAIRequestToAnthropic([]byte(oai), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("forward conversion failed: %v", err)
	}
	oaiReq, err := AnthropicRequestToOpenAI(anthReq, "gpt-4o")
	if err != nil {
		t.Fatalf("back conversion failed: %v", err)
	}
	blocks := anthUserBlocks(t, anthReq)
	if len(blocks) != 3 {
		t.Fatalf("forward conversion lost attachments: %d blocks", len(blocks))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(oaiReq, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	msgs := result["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 3 {
		t.Fatalf("round trip lost attachments: %d parts", len(content))
	}
	file := content[0].(map[string]interface{})["file"].(map[string]interface{})
	if file["file_data"] != "data:application/pdf;base64,"+pdfB64 {
		t.Errorf("file_data changed across round trip: %v", file["file_data"])
	}
	audio := content[1].(map[string]interface{})["input_audio"].(map[string]interface{})
	if audio["format"] != "wav" || audio["data"] != "V0FWRQ==" {
		t.Errorf("audio changed across round trip: %v", audio)
	}
	video := content[2].(map[string]interface{})["video_url"].(map[string]interface{})
	if video["url"] != "https://example.com/clip.mp4" {
		t.Errorf("video url changed across round trip: %v", video["url"])
	}
}

func TestAttachmentsInToolResults(t *testing.T) {
	// Regression: file-reading tools return attachments inside tool_result
	// content — those must also convert instead of shipping Anthropic-shaped
	// blocks into OpenAI tool messages (and vice versa).
	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n"))

	t.Run("anthropic tool_result document becomes openai tool file part", func(t *testing.T) {
		oaiReq, err := AnthropicRequestToOpenAI([]byte(anthUserMsg(
			`{"type":"tool_result","tool_use_id":"call_1","content":`+
				`[{"type":"text","text":"file contents:"},`+
				`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"`+pdfB64+`"}}]}`)), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(oaiReq, &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		msgs := result["messages"].([]interface{})
		var toolMsg map[string]interface{}
		for _, m := range msgs {
			if mm, ok := m.(map[string]interface{}); ok && mm["role"] == "tool" {
				toolMsg = mm
				break
			}
		}
		if toolMsg == nil {
			t.Fatal("no tool message in converted request")
		}
		content := toolMsg["content"].([]interface{})
		if len(content) != 2 {
			t.Fatalf("expected 2 content parts, got %d: %v", len(content), content)
		}
		filePart := content[1].(map[string]interface{})
		if filePart["type"] != "file" {
			t.Errorf("part type = %v, want file", filePart["type"])
		}
		file := filePart["file"].(map[string]interface{})
		if file["file_data"] != "data:application/pdf;base64,"+pdfB64 {
			t.Errorf("file_data = %v", file["file_data"])
		}
	})

	t.Run("openai tool file part becomes anthropic tool_result document", func(t *testing.T) {
		anthReq, err := OpenAIRequestToAnthropic([]byte(`{"model":"gpt-4o","messages":[`+
			`{"role":"assistant","content":"reading","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},`+
			`{"role":"tool","tool_call_id":"call_1","content":[`+
			`{"type":"file","file":{"file_data":"data:application/pdf;base64,`+pdfB64+`"}}]}]}`), "claude-sonnet-4")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(anthReq, &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		msgs := result["messages"].([]interface{})
		var userMsg map[string]interface{}
		for _, m := range msgs {
			if mm, ok := m.(map[string]interface{}); ok && mm["role"] == "user" {
				userMsg = mm
				break
			}
		}
		if userMsg == nil {
			t.Fatal("no user message in converted request")
		}
		content := userMsg["content"].([]interface{})
		if len(content) != 1 {
			t.Fatalf("expected 1 block, got %d", len(content))
		}
		tr := content[0].(map[string]interface{})
		if tr["type"] != "tool_result" {
			t.Fatalf("block type = %v, want tool_result", tr["type"])
		}
		trContent := tr["content"].([]interface{})
		if len(trContent) != 1 {
			t.Fatalf("expected 1 content block in tool_result, got %d", len(trContent))
		}
		doc := trContent[0].(map[string]interface{})
		if doc["type"] != "document" {
			t.Errorf("tool_result block type = %v, want document", doc["type"])
		}
		source := doc["source"].(map[string]interface{})
		if source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != pdfB64 {
			t.Errorf("unexpected source: %v", source)
		}
	})

	t.Run("all-dropped openai tool content becomes empty string", func(t *testing.T) {
		// A file_id-only part is dropped by design; the resulting tool_result
		// content must be "" — never null — since Anthropic requires a
		// string or array.
		anthReq, err := OpenAIRequestToAnthropic([]byte(`{"model":"gpt-4o","messages":[`+
			`{"role":"assistant","content":"reading","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},`+
			`{"role":"tool","tool_call_id":"call_1","content":[{"type":"file","file":{"file_id":"file-abc123"}}]}]}`), "claude-sonnet-4")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(anthReq, &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		msgs := result["messages"].([]interface{})
		var userMsg map[string]interface{}
		for _, m := range msgs {
			if mm, ok := m.(map[string]interface{}); ok && mm["role"] == "user" {
				userMsg = mm
				break
			}
		}
		if userMsg == nil {
			t.Fatal("no user message in converted request")
		}
		content := userMsg["content"].([]interface{})
		if len(content) != 1 {
			t.Fatalf("expected 1 block, got %d", len(content))
		}
		tr := content[0].(map[string]interface{})
		if tr["type"] != "tool_result" {
			t.Fatalf("block type = %v, want tool_result", tr["type"])
		}
		if tr["content"] != "" {
			t.Errorf("tool_result content = %v, want empty string", tr["content"])
		}
	})

	t.Run("openai tool text part with null/missing text becomes empty text block", func(t *testing.T) {
		// The shared convertOpenAIContentArray also feeds the tool path —
		// a text part with null/missing text must not ship text:null
		// inside tool_result.content. The block survives as an empty text
		// block (not the "" string fallback, since len > 0).
		for _, content := range []string{
			`[{"type":"text","text":null}]`,
			`[{"type":"text"}]`,
		} {
			anthReq, err := OpenAIRequestToAnthropic([]byte(`{"model":"gpt-4o","messages":[`+
				`{"role":"assistant","content":"reading","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},`+
				`{"role":"tool","tool_call_id":"call_1","content":`+content+`}]}`), "claude-sonnet-4")
			if err != nil {
				t.Fatalf("conversion failed: %v", err)
			}
			if strings.Contains(string(anthReq), `"text":null`) {
				t.Fatalf("output still contains text:null for content %s: %s", content, anthReq)
			}
			var result map[string]interface{}
			if err := json.Unmarshal(anthReq, &result); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			msgs := result["messages"].([]interface{})
			var userMsg map[string]interface{}
			for _, m := range msgs {
				if mm, ok := m.(map[string]interface{}); ok && mm["role"] == "user" {
					userMsg = mm
					break
				}
			}
			if userMsg == nil {
				t.Fatal("no user message in converted request")
			}
			blocks := userMsg["content"].([]interface{})
			if len(blocks) != 1 {
				t.Fatalf("expected 1 block, got %d", len(blocks))
			}
			tr := blocks[0].(map[string]interface{})
			if tr["type"] != "tool_result" {
				t.Fatalf("block type = %v, want tool_result", tr["type"])
			}
			trContent, ok := tr["content"].([]interface{})
			if !ok || len(trContent) != 1 {
				t.Fatalf("tool_result content = %v (%T), want a single text block array", tr["content"], tr["content"])
			}
			textBlock := trContent[0].(map[string]interface{})
			if textBlock["type"] != "text" {
				t.Fatalf("inner block type = %v, want text", textBlock["type"])
			}
			if got, ok := textBlock["text"].(string); !ok || got != "" {
				t.Errorf("inner text = %v (%T), want empty string", textBlock["text"], textBlock["text"])
			}
		}
	})

	t.Run("all-dropped anthropic tool_result content becomes empty string", func(t *testing.T) {
		oaiReq, err := AnthropicRequestToOpenAI([]byte(anthUserMsg(
			`{"type":"tool_result","tool_use_id":"call_1","content":`+
				`[{"type":"audio","source":{"type":"url","url":"https://example.com/a.mp3"}}]}`)), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(oaiReq, &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		msgs := result["messages"].([]interface{})
		var toolMsg map[string]interface{}
		for _, m := range msgs {
			if mm, ok := m.(map[string]interface{}); ok && mm["role"] == "tool" {
				toolMsg = mm
				break
			}
		}
		if toolMsg == nil {
			t.Fatal("no tool message in converted request")
		}
		if toolMsg["content"] != "" {
			t.Errorf("tool message content = %v, want empty string", toolMsg["content"])
		}
	})
}
