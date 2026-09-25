//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPrivateFallbackDataDir(path string) error {
	security, _, err := privateFallbackSecurityDescriptor()
	if err != nil {
		return err
	}
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: security,
	}
	if err := windows.CreateDirectory(pathPtr, attributes); err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func privateFallbackDataDir(path string, _ os.FileInfo) bool {
	expected, currentUser, err := privateFallbackSecurityDescriptor()
	if err != nil {
		return false
	}
	trusted, err := trustedFallbackWindowsSIDs(currentUser)
	if err != nil {
		return false
	}
	paths := []string{path}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		info, err := os.Lstat(child)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false
		}
		paths = append(paths, child)
	}

	for _, object := range paths {
		if !windowsObjectACLTrusted(object, currentUser, trusted) {
			return false
		}
	}

	privateDACL, _, err := expected.DACL()
	if err != nil {
		return false
	}
	for _, object := range paths {
		if windows.SetNamedSecurityInfo(object, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, privateDACL, nil) != nil {
			return false
		}
	}
	return true
}

func validateFallbackTempParent(dir string) error {
	dirs, err := fallbackTempParentDirs(dir)
	if err != nil {
		return err
	}
	_, currentUser, err := privateFallbackSecurityDescriptor()
	if err != nil {
		return err
	}
	trusted, err := trustedFallbackWindowsSIDs(currentUser)
	if err != nil {
		return err
	}
	for _, path := range dirs {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("temporary path component %q is not a real directory", path)
		}
		owner, dacl, err := windowsObjectSecurity(path)
		if err != nil {
			return err
		}
		if !owner.Equals(currentUser) && !owner.Equals(trusted[1]) && !owner.Equals(trusted[2]) && !owner.Equals(trusted[4]) {
			return fmt.Errorf("temporary path component %q has an untrusted owner", path)
		}
		if !windowsParentACLNonReplaceable(dacl, trusted) {
			return fmt.Errorf("temporary path component %q grants untrusted replacement rights", path)
		}
	}
	return nil
}

func windowsObjectSecurity(path string) (*windows.SID, *windows.ACL, error) {
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, nil, err
	}
	owner, _, err := security.Owner()
	if err != nil {
		return nil, nil, err
	}
	dacl, _, err := security.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return nil, nil, fmt.Errorf("%q has no restrictive DACL", path)
	}
	return owner, dacl, nil
}

func trustedFallbackWindowsSIDs(currentUser *windows.SID) ([]*windows.SID, error) {
	values := []*windows.SID{currentUser}
	for _, text := range []string{"S-1-5-18", "S-1-5-32-544", "S-1-3-0", "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"} {
		sid, err := windows.StringToSid(text)
		if err != nil {
			return nil, err
		}
		values = append(values, sid)
	}
	return values, nil
}

func windowsObjectACLTrusted(path string, currentUser *windows.SID, trusted []*windows.SID) bool {
	owner, dacl, err := windowsObjectSecurity(path)
	if err != nil || !owner.Equals(currentUser) {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windowsSIDInList(sid, trusted) {
			return false
		}
	}
	return true
}

func windowsParentACLNonReplaceable(dacl *windows.ACL, trusted []*windows.SID) bool {
	const fileDeleteChild windows.ACCESS_MASK = 0x40
	const replacementRights = windows.ACCESS_MASK(windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_ALL) | fileDeleteChild
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windowsSIDInList(sid, trusted) && windows.ACCESS_MASK(ace.Mask)&replacementRights != 0 {
			return false
		}
	}
	return true
}

func windowsSIDInList(sid *windows.SID, values []*windows.SID) bool {
	for _, value := range values {
		if sid.Equals(value) {
			return true
		}
	}
	return false
}

func privateFallbackSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	userSID := tokenUser.User.Sid
	userSIDString := userSID.String()
	sddl := fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", userSIDString, userSIDString)
	security, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, err
	}
	return security, userSID, nil
}
