package webhook

import "testing"

func TestVerify(t *testing.T) {
	secret := []byte("It's a Secret to Everybody")
	body := []byte("Hello, World!")
	// Independent vector: openssl dgst -sha256 -hmac <secret> over <body>.
	const good = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	cases := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   bool
	}{
		{"valid", secret, body, good, true},
		{"wrong secret", []byte("other"), body, good, false},
		{"tampered body", secret, []byte("Hello, World?"), good, false},
		{"missing header", secret, body, "", false},
		{"missing prefix", secret, body, good[len("sha256="):], false},
		{"wrong algorithm", secret, body, "sha1=" + good[len("sha256="):], false},
		{"not hex", secret, body, "sha256=zzzz", false},
		{"truncated", secret, body, good[:len(good)-2], false},
		{"empty secret never verifies", nil, body, good, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Verify(tc.secret, tc.body, tc.header); got != tc.want {
				t.Fatalf("Verify = %v, want %v", got, tc.want)
			}
		})
	}
}
