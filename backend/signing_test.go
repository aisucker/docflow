package main

import (
	"bytes"
	"context"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Protocol fixtures for a mocked verifier. They are not cryptographic signatures.
func cmsFixture(attached bool) []byte {
	content := struct {
		Type asn1.ObjectIdentifier
		Data asn1.RawValue `asn1:"optional,explicit,tag:0"`
	}{Type: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}}
	if attached {
		encoded, _ := asn1.Marshal([]byte("unrelated"))
		content.Data = asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: encoded}
	}
	sd := struct {
		Version int
		Digests asn1.RawValue
		Content any
		Signers asn1.RawValue
	}{1, asn1.RawValue{Tag: 17, IsCompound: true}, content, asn1.RawValue{Tag: 17, IsCompound: true}}
	raw, _ := asn1.Marshal(sd)
	ci := struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue
	}{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: raw}}
	result, _ := asn1.Marshal(ci)
	return result
}
func goodNCAReport() map[string]any {
	return map[string]any{"status": 200, "valid": true, "signers": []any{map[string]any{"status": "VALID", "certificates": []any{map[string]any{"valid": true, "keyUsage": "SIGN", "serialNumber": "abc123", "notBefore": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "notAfter": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "subject": map[string]any{"iin": "123456789012", "commonName": "Test Signer", "country": "KZ"}, "revocations": []any{map[string]any{"by": "OCSP", "revoked": false}, map[string]any{"by": "CRL", "revoked": false}}}}}}}
}
func TestDetachedCMS(t *testing.T) {
	if !detachedCMS(cmsFixture(false)) {
		t.Fatal("detached rejected")
	}
	if detachedCMS(cmsFixture(true)) || detachedCMS([]byte("garbage")) {
		t.Fatal("invalid CMS accepted")
	}
}

func TestCMSDecoding(t *testing.T) {
	raw := cmsFixture(false)
	encoded := base64.StdEncoding.EncodeToString(raw)
	wrapped := encoded[:12] + "\r\n \t" + encoded[12:]
	for _, input := range []string{encoded, wrapped, string(pem.EncodeToMemory(&pem.Block{Type: "CMS", Bytes: raw})), string(pem.EncodeToMemory(&pem.Block{Type: "PKCS7", Bytes: raw}))} {
		got, err := decodeCMS(input)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("CMS transport decoding failed: %v", err)
		}
	}
	for _, input := range []string{"not a signature", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})), string(pem.EncodeToMemory(&pem.Block{Type: "CMS", Bytes: raw})) + "unexpected"} {
		if _, err := decodeCMS(input); err == nil {
			t.Fatal("invalid CMS transport accepted")
		}
	}
}
func TestVerifyNCAFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   bool
	}{
		{"valid", func(m map[string]any) {}, true},
		{"invalid", func(m map[string]any) { m["valid"] = false }, false},
		{"no signers", func(m map[string]any) { m["signers"] = []any{} }, false},
		{"unknown trust", func(m map[string]any) { m["signers"].([]any)[0].(map[string]any)["status"] = "INDETERMINATE" }, false},
		{"wrong IIN", func(m map[string]any) {
			m["signers"].([]any)[0].(map[string]any)["certificates"].([]any)[0].(map[string]any)["subject"].(map[string]any)["iin"] = "999999999999"
		}, false},
		{"expired", func(m map[string]any) {
			m["signers"].([]any)[0].(map[string]any)["certificates"].([]any)[0].(map[string]any)["notAfter"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		}, false},
		{"revoked", func(m map[string]any) {
			m["signers"].([]any)[0].(map[string]any)["certificates"].([]any)[0].(map[string]any)["revocations"] = []any{map[string]any{"by": "OCSP", "revoked": true}}
		}, false},
		{"missing revocation", func(m map[string]any) {
			m["signers"].([]any)[0].(map[string]any)["certificates"].([]any)[0].(map[string]any)["revocations"] = []any{}
		}, false},
		{"authentication certificate", func(m map[string]any) {
			m["signers"].([]any)[0].(map[string]any)["certificates"].([]any)[0].(map[string]any)["keyUsage"] = "AUTH"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := goodNCAReport()
			tc.change(response)
			verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/cms/verify" {
					t.Error(r.URL.Path)
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["data"] != base64.StdEncoding.EncodeToString([]byte("payload")) {
					t.Error("wrong verification payload")
				}
				if len(body["revocationCheck"].([]any)) != 2 {
					t.Error("revocation checking disabled")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}))
			defer verifier.Close()
			t.Setenv("NCANODE_URL", verifier.URL)
			_, _, err := verifyNCA(context.Background(), cmsFixture(false), []byte("payload"), "123456789012")
			if (err == nil) != tc.want {
				t.Fatalf("got %v", err)
			}
			if _, _, err = verifyNCA(context.Background(), cmsFixture(true), []byte("payload"), "123456789012"); err == nil {
				t.Fatal("attached CMS allowed")
			}
		})
	}
}
