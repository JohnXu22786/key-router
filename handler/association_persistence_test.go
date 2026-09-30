package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"key-router/db"
	"key-router/model"
)

func createAssociationTestProvider(t *testing.T, name string) model.Provider {
	t.Helper()
	provider := model.Provider{Name: name, Type: "openai", BaseURL: "http://" + name}
	if err := db.GetDB().Create(&provider).Error; err != nil {
		t.Fatal(err)
	}
	return provider
}

func createAssociationTestGroup(t *testing.T, groupID string) model.ModelGroup {
	t.Helper()
	group := model.ModelGroup{GroupID: groupID, Name: groupID, Enabled: true}
	if err := db.GetDB().Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	return group
}

func sendAssociationTestRequest(t *testing.T, e http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestCreateKeyPersistsValidatedProviderID(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	validated := createAssociationTestProvider(t, "validated")
	mismatched := createAssociationTestProvider(t, "mismatched")
	rec := sendAssociationTestRequest(t, e, http.MethodPost, "/api/keys", map[string]any{
		"provider_id": validated.ID,
		"name":        "key",
		"key_value":   "key-value",
		"provider":    map[string]any{"id": mismatched.ID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/keys status = %d: %s", rec.Code, rec.Body.String())
	}
	var response model.Key
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProviderID != validated.ID || response.Provider.ID != validated.ID {
		t.Errorf("response provider IDs = scalar %d, nested %d; want %d",
			response.ProviderID, response.Provider.ID, validated.ID)
	}

	var stored model.Key
	if err := db.GetDB().Where("key_value = ?", "key-value").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ProviderID != validated.ID {
		t.Errorf("stored provider_id = %d, want validated scalar ID %d", stored.ProviderID, validated.ID)
	}
}

func TestCreateRoutePersistsValidatedForeignKeys(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	validatedGroup := createAssociationTestGroup(t, "validated-group")
	mismatchedGroup := createAssociationTestGroup(t, "mismatched-group")
	validatedProvider := createAssociationTestProvider(t, "validated-provider")
	mismatchedProvider := createAssociationTestProvider(t, "mismatched-provider")
	rec := sendAssociationTestRequest(t, e, http.MethodPost, "/api/routes", map[string]any{
		"model_group_id": validatedGroup.ID,
		"provider_id":    validatedProvider.ID,
		"target_model":   "target",
		"model_group":    map[string]any{"id": mismatchedGroup.ID},
		"provider":       map[string]any{"id": mismatchedProvider.ID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/routes status = %d: %s", rec.Code, rec.Body.String())
	}
	var response model.Route
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ModelGroupID != validatedGroup.ID || response.ModelGroup.ID != validatedGroup.ID ||
		response.ProviderID != validatedProvider.ID || response.Provider.ID != validatedProvider.ID {
		t.Errorf("response foreign keys = scalar/nested group %d/%d, provider %d/%d; want %d/%d",
			response.ModelGroupID, response.ModelGroup.ID, response.ProviderID, response.Provider.ID,
			validatedGroup.ID, validatedProvider.ID)
	}

	var stored model.Route
	if err := db.GetDB().Where("target_model = ?", "target").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ModelGroupID != validatedGroup.ID || stored.ProviderID != validatedProvider.ID {
		t.Errorf("stored foreign keys = model_group_id %d, provider_id %d; want %d, %d",
			stored.ModelGroupID, stored.ProviderID, validatedGroup.ID, validatedProvider.ID)
	}
}

func TestUpdateRoutePersistsValidatedForeignKeys(t *testing.T) {
	e := bootstrapKeys(t)
	closeTestDB(t)

	initialGroup := createAssociationTestGroup(t, "initial-group")
	validatedGroup := createAssociationTestGroup(t, "validated-group")
	mismatchedGroup := createAssociationTestGroup(t, "mismatched-group")
	initialProvider := createAssociationTestProvider(t, "initial-provider")
	validatedProvider := createAssociationTestProvider(t, "validated-provider")
	mismatchedProvider := createAssociationTestProvider(t, "mismatched-provider")
	route := model.Route{
		ModelGroupID: initialGroup.ID,
		ProviderID:   initialProvider.ID,
		TargetModel:  "target",
		Priority:     1,
		Weight:       10,
		Enabled:      true,
	}
	if err := db.GetDB().Create(&route).Error; err != nil {
		t.Fatal(err)
	}

	rec := sendAssociationTestRequest(t, e, http.MethodPut, "/api/routes/"+jsonInt(route.ID), map[string]any{
		"model_group_id": validatedGroup.ID,
		"provider_id":    validatedProvider.ID,
		"model_group":    map[string]any{"id": mismatchedGroup.ID},
		"provider":       map[string]any{"id": mismatchedProvider.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/routes/%d status = %d: %s", route.ID, rec.Code, rec.Body.String())
	}
	var response model.Route
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ModelGroupID != validatedGroup.ID || response.ModelGroup.ID != validatedGroup.ID ||
		response.ProviderID != validatedProvider.ID || response.Provider.ID != validatedProvider.ID {
		t.Errorf("response foreign keys = scalar/nested group %d/%d, provider %d/%d; want %d/%d",
			response.ModelGroupID, response.ModelGroup.ID, response.ProviderID, response.Provider.ID,
			validatedGroup.ID, validatedProvider.ID)
	}

	var stored model.Route
	if err := db.GetDB().First(&stored, route.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ModelGroupID != validatedGroup.ID || stored.ProviderID != validatedProvider.ID {
		t.Errorf("stored foreign keys = model_group_id %d, provider_id %d; want %d, %d",
			stored.ModelGroupID, stored.ProviderID, validatedGroup.ID, validatedProvider.ID)
	}
}
