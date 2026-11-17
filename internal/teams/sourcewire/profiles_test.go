package sourcewire

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProfileAndTypedBindingVector(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/source-profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Page             ProfilePage          `json:"page"`
		PageJSON         string               `json:"page_json"`
		PageHash         string               `json:"page_sha256"`
		Seal             ProfileSeal          `json:"seal"`
		SealJSON         string               `json:"seal_json"`
		SealHash         string               `json:"seal_sha256"`
		Binding          TypedBindingEnvelope `json:"binding"`
		BindingJSON      string               `json:"binding_json"`
		BindingHash      string               `json:"binding_sha256"`
		Assignment       TypedAssignmentPage  `json:"assignment_page"`
		AssignmentJSON   string               `json:"assignment_page_json"`
		AssignmentHash   string               `json:"assignment_page_sha256"`
		AcceptedPage     ProfilePageReceipt   `json:"accepted_page_receipt"`
		AcceptedPageJSON string               `json:"accepted_page_receipt_json"`
		HeldPage         ProfilePageReceipt   `json:"held_page_receipt"`
		HeldPageJSON     string               `json:"held_page_receipt_json"`
		AcceptedSeal     ProfileSealReceipt   `json:"accepted_seal_receipt"`
		AcceptedSealJSON string               `json:"accepted_seal_receipt_json"`
		HeldSeal         ProfileSealReceipt   `json:"held_seal_receipt"`
		HeldSealJSON     string               `json:"held_seal_receipt_json"`
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		bytes      func() ([]byte, string, error)
		want, hash string
	}{{f.Page.Bytes, f.PageJSON, f.PageHash}, {f.Seal.Bytes, f.SealJSON, f.SealHash}, {f.Binding.Bytes, f.BindingJSON, f.BindingHash}, {f.Assignment.Bytes, f.AssignmentJSON, f.AssignmentHash}} {
		b, h, e := v.bytes()
		if e != nil || string(b) != v.want || h != v.hash {
			t.Fatalf("canonical profile vector mismatch: %v", e)
		}
	}
	for _, v := range []struct {
		value any
		want  string
	}{{f.AcceptedPage, f.AcceptedPageJSON}, {f.HeldPage, f.HeldPageJSON}, {f.AcceptedSeal, f.AcceptedSealJSON}, {f.HeldSeal, f.HeldSealJSON}} {
		b, e := json.Marshal(v.value)
		if e != nil || string(b) != v.want {
			t.Fatal("profile receipt vector mismatch")
		}
	}
	if err := VerifyTypedAssignmentPages(f.BindingHash, f.Binding.Profiles[0], []TypedAssignmentPage{f.Assignment}); err != nil {
		t.Fatalf("assignment commitment: %v", err)
	}
	// Even empty profiles commit to exactly one empty content page.
	empty := f.Assignment
	empty.Items = []TypedAssignment{}
	_, emptyHash, e := empty.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	emptyRef := f.Binding.Profiles[0]
	emptyRef.AssignmentItemCount = 0
	emptyRef.AssignmentSHA256 = emptyHash
	if VerifyTypedAssignmentPages(f.BindingHash, emptyRef, []TypedAssignmentPage{empty}) != nil || VerifyTypedAssignmentPages(f.BindingHash, emptyRef, nil) == nil {
		t.Fatal("empty assignment chain")
	}
	// Pages from different issuances cannot be spliced into a complete chain.
	first := f.Assignment
	first.PageCount = 2
	_, firstHash, e := first.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	second := first
	second.Index = 1
	second.PreviousPageSHA256 = &firstHash
	second.Items = append([]TypedAssignment(nil), first.Items...)
	second.Items[0].Token = "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI"
	_, lastHash, e := second.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	pairRef := f.Binding.Profiles[0]
	pairRef.AssignmentPageCount, pairRef.AssignmentItemCount, pairRef.AssignmentSHA256 = 2, 2, lastHash
	if VerifyTypedAssignmentPages(f.BindingHash, pairRef, []TypedAssignmentPage{first, second}) != nil {
		t.Fatal("valid assignment chain rejected")
	}
	other := second
	other.Items = append([]TypedAssignment(nil), second.Items...)
	other.Items[0].Decision = "excluded"
	if VerifyTypedAssignmentPages(f.BindingHash, pairRef, []TypedAssignmentPage{first, other}) == nil || VerifyTypedAssignmentPages(f.BindingHash, pairRef, []TypedAssignmentPage{second, first}) == nil {
		t.Fatal("mixed assignment chain accepted")
	}
	badAssignment := f.Assignment
	badAssignment.Items = append([]TypedAssignment(nil), f.Assignment.Items...)
	badAssignment.Items[0].Decision = "excluded"
	if VerifyTypedAssignmentPages(f.BindingHash, f.Binding.Profiles[0], []TypedAssignmentPage{badAssignment}) == nil {
		t.Fatal("changed assignment accepted")
	}
	badAssignment = f.Assignment
	badAssignment.BindingSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if VerifyTypedAssignmentPages(f.BindingHash, f.Binding.Profiles[0], []TypedAssignmentPage{badAssignment}) == nil {
		t.Fatal("foreign binding accepted")
	}
	badAssignment = f.Assignment
	badAssignment.Items = append([]TypedAssignment(nil), f.Assignment.Items...)
	badAssignment.Items[0].Classification.PolicySHA256 = ""
	if badAssignment.Validate() == nil {
		t.Fatal("required classification without policy accepted")
	}
	if _, e := DecodeProfilePage([]byte(f.PageJSON)); e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeProfileSeal([]byte(f.SealJSON)); e != nil {
		t.Fatal(e)
	}
	bad := f.Page
	bad.Items[0].Dependencies = []string{bad.Items[0].Token}
	if bad.Validate() == nil {
		t.Fatal("self dependency accepted")
	}
	bad = f.Page
	bad.Index = 1
	if bad.Validate() == nil {
		t.Fatal("missing previous page digest accepted")
	}
	v := f.Binding
	v.Version = 1
	if v.Validate() == nil {
		t.Fatal("legacy binding accepted")
	}
}
