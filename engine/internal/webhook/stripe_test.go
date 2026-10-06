package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestStripeVerifySignature(t *testing.T) {
	secret := []byte("whsec_test_secret")
	payload := []byte(`{"id":"evt_123","type":"payment_intent.succeeded"}`)

	// Create a valid signature.
	ts := time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", ts, payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signedPayload))
	validSig := hex.EncodeToString(mac.Sum(nil))
	validHeader := fmt.Sprintf("t=%d,v1=%s", ts, validSig)

	p := &stripeProvider{}

	tests := []struct {
		name    string
		params  SignatureParams
		wantErr bool
	}{
		{
			name: "valid signature",
			params: SignatureParams{
				Header:  validHeader,
				Secret:  secret,
				Payload: payload,
			},
			wantErr: false,
		},
		{
			name: "missing header",
			params: SignatureParams{
				Header:  "",
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "empty secret",
			params: SignatureParams{
				Header:  validHeader,
				Secret:  nil,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "wrong signature",
			params: SignatureParams{
				Header:  fmt.Sprintf("t=%d,v1=wrongsig", ts),
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "missing timestamp",
			params: SignatureParams{
				Header:  fmt.Sprintf("v1=%s", validSig),
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "timestamp too old",
			params: SignatureParams{
				Header: func() string {
					oldTs := time.Now().Add(-10 * time.Minute).Unix()
					oldSigned := fmt.Sprintf("%d.%s", oldTs, payload)
					mac := hmac.New(sha256.New, secret)
					mac.Write([]byte(oldSigned))
					return fmt.Sprintf("t=%d,v1=%s", oldTs, hex.EncodeToString(mac.Sum(nil)))
				}(),
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := p.VerifySignature(tc.params)
			if (err != nil) != tc.wantErr {
				t.Errorf("VerifySignature() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestStripeParseEvent(t *testing.T) {
	payload := []byte(`{"id":"evt_123","type":"payment_intent.succeeded","data":{"object":{"id":"pi_123","amount":1000}}}`)
	headers := map[string]string{"Content-Type": "application/json"}

	p := &stripeProvider{}
	event, err := p.ParseEvent(payload, headers)
	if err != nil {
		t.Fatalf("ParseEvent() error = %v", err)
	}

	if event.Type != "payment_intent.succeeded" {
		t.Errorf("Type = %q, want %q", event.Type, "payment_intent.succeeded")
	}
	if event.Payload["id"] != "evt_123" {
		t.Errorf("Payload id = %v, want %q", event.Payload["id"], "evt_123")
	}
}

func TestStripeMultipleSignatures(t *testing.T) {
	secret := []byte("whsec_test_secret")
	payload := []byte(`{"id":"evt_123","type":"test"}`)
	ts := time.Now().Unix()

	signedPayload := strconv.FormatInt(ts, 10) + "." + string(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signedPayload))
	validSig := hex.EncodeToString(mac.Sum(nil))

	// Multiple v1 signatures (key rotation scenario).
	header := fmt.Sprintf("t=%d,v1=invalid,v1=%s", ts, validSig)

	p := &stripeProvider{}
	err := p.VerifySignature(SignatureParams{
		Header:  header,
		Secret:  secret,
		Payload: payload,
	})
	if err != nil {
		t.Errorf("VerifySignature with multiple sigs should succeed: %v", err)
	}
}
