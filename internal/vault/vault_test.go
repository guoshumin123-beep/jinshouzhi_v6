package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// 测试中的明文均为假数据；断言失败时只报告“是否相等”，绝不打印明文。

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func newVault(t *testing.T) *Vault {
	t.Helper()
	v, err := New(map[string][]byte{"k1": randKey(t)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var (
	fakeSecret = []byte("fake-secret-0000-1111-2222")
	aadA       = []byte("subaccount:1001:fake-ak-a")
	aadB       = []byte("subaccount:1002:fake-ak-b")
)

func TestRoundTrip(t *testing.T) {
	v := newVault(t)
	s, err := v.Seal(fakeSecret, aadA)
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyID != "k1" || len(s.Nonce) != nonceSize || bytes.Contains(s.Ciphertext, fakeSecret) {
		t.Fatal("sealed output malformed or contains plaintext")
	}
	pt, err := v.Open(s, aadA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, fakeSecret) {
		t.Fatal("decrypted plaintext mismatch")
	}
	Wipe(pt)
	if !bytes.Equal(pt, make([]byte, len(pt))) {
		t.Fatal("wipe did not zero buffer")
	}
}

func TestAADBindsCiphertextToRecord(t *testing.T) {
	v := newVault(t)
	s, _ := v.Seal(fakeSecret, aadA)
	if _, err := v.Open(s, aadB); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("ciphertext moved to another record must fail, got %v", err)
	}
	if _, err := v.Seal(fakeSecret, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("empty aad must be refused, got %v", err)
	}
}

func TestTamperingDetected(t *testing.T) {
	v := newVault(t)
	s, _ := v.Seal(fakeSecret, aadA)
	cases := map[string]func(Sealed) Sealed{
		"flip ciphertext bit": func(s Sealed) Sealed {
			c := append([]byte(nil), s.Ciphertext...)
			c[0] ^= 1
			s.Ciphertext = c
			return s
		},
		"flip tag bit": func(s Sealed) Sealed {
			c := append([]byte(nil), s.Ciphertext...)
			c[len(c)-1] ^= 1
			s.Ciphertext = c
			return s
		},
		"flip nonce bit": func(s Sealed) Sealed {
			n := append([]byte(nil), s.Nonce...)
			n[0] ^= 1
			s.Nonce = n
			return s
		},
		"truncate":    func(s Sealed) Sealed { s.Ciphertext = s.Ciphertext[:5]; return s },
		"short nonce": func(s Sealed) Sealed { s.Nonce = s.Nonce[:4]; return s },
	}
	for name, mut := range cases {
		if _, err := v.Open(mut(s), aadA); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: want ErrDecrypt, got %v", name, err)
		}
	}
}

func TestKeyIDBoundIntoAAD(t *testing.T) {
	k := randKey(t)
	// 两个 id 指向同一把密钥：改写 KeyID 仍应失败（id 参与 AAD）
	v, err := New(map[string][]byte{"k1": k, "k2": k}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := v.Seal(fakeSecret, aadA)
	s.KeyID = "k2"
	if _, err := v.Open(s, aadA); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("rewritten key id must fail, got %v", err)
	}
	s.KeyID = "nope"
	if _, err := v.Open(s, aadA); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key id: %v", err)
	}
}

func TestNonceUniqueness(t *testing.T) {
	v := newVault(t)
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		s, err := v.Seal(fakeSecret, aadA)
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(s.Nonce)] {
			t.Fatal("nonce reused")
		}
		seen[string(s.Nonce)] = true
	}
}

func TestKeyRotation(t *testing.T) {
	old, cur := randKey(t), randKey(t)
	v1, _ := New(map[string][]byte{"k1": old}, "k1")
	s, _ := v1.Seal(fakeSecret, aadA)

	v2, err := New(map[string][]byte{"k1": old, "k2": cur}, "k2")
	if err != nil {
		t.Fatal(err)
	}
	if !v2.NeedsRotation(s) {
		t.Fatal("old ciphertext should need rotation")
	}
	pt, err := v2.Open(s, aadA)
	if err != nil || !bytes.Equal(pt, fakeSecret) {
		t.Fatal("old key must still decrypt after rotation")
	}
	rs, err := v2.Reseal(s, aadA)
	if err != nil || rs.KeyID != "k2" || v2.NeedsRotation(rs) {
		t.Fatalf("reseal: key=%s err=%v", rs.KeyID, err)
	}
	// 移除旧密钥后，重新加密的密文仍可解，旧密文不可解
	v3, _ := New(map[string][]byte{"k2": cur}, "k2")
	if pt, err := v3.Open(rs, aadA); err != nil || !bytes.Equal(pt, fakeSecret) {
		t.Fatal("resealed ciphertext must decrypt with new key only")
	}
	if _, err := v3.Open(s, aadA); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("old ciphertext without old key: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(map[string][]byte{"k1": make([]byte, 16)}, "k1"); !errors.Is(err, ErrConfig) {
		t.Fatalf("16-byte key accepted: %v", err)
	}
	if _, err := New(map[string][]byte{"k1": randKey(t)}, "k2"); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing active key accepted: %v", err)
	}
	if _, err := New(map[string][]byte{"bad-id!": randKey(t)}, "bad-id!"); !errors.Is(err, ErrConfig) {
		t.Fatalf("bad key id accepted: %v", err)
	}
}

func TestFromEnviron(t *testing.T) {
	k1, k2 := randKey(t), randKey(t)
	env := []string{
		"PATH=/usr/bin",
		"VAULT_ACTIVE_KEY_ID=2026q4",
		"VAULT_KEY_2026q3=" + base64.StdEncoding.EncodeToString(k1),
		"VAULT_KEY_2026q4=" + base64.StdEncoding.EncodeToString(k2),
	}
	v, err := fromEnviron(env, "2026q4")
	if err != nil {
		t.Fatal(err)
	}
	if v.ActiveKeyID() != "2026q4" || fmt.Sprint(v.KeyIDs()) != "[2026q3 2026q4]" {
		t.Fatalf("loaded %v", v)
	}
	if _, err := fromEnviron(env, ""); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing active id: %v", err)
	}
	badVal := "not*base64*value"
	_, err = fromEnviron([]string{"VAULT_KEY_x=" + badVal}, "x")
	if !errors.Is(err, ErrConfig) || strings.Contains(err.Error(), badVal) {
		t.Fatalf("bad base64 must fail without echoing the value: %v", err)
	}
	if _, err := fromEnviron([]string{"PATH=/bin"}, "x"); !errors.Is(err, ErrConfig) {
		t.Fatalf("no keys: %v", err)
	}

	// 真实环境变量路径
	t.Setenv(EnvActiveKeyID, "envk")
	t.Setenv(EnvKeyPrefix+"envk", base64.StdEncoding.EncodeToString(randKey(t)))
	if v, err := FromEnv(); err != nil || v.ActiveKeyID() != "envk" {
		t.Fatalf("FromEnv: %v %v", v, err)
	}
}

func TestNoSecretLeakInOutputs(t *testing.T) {
	k := randKey(t)
	v, _ := New(map[string][]byte{"k1": k}, "k1")
	b64 := base64.StdEncoding.EncodeToString(k)
	s, _ := v.Seal(fakeSecret, aadA)
	_, decErr := v.Open(s, aadB)
	for _, out := range []string{fmt.Sprint(v), fmt.Sprintf("%+v %#v", v, v), decErr.Error()} {
		if strings.Contains(out, b64) || strings.Contains(out, string(fakeSecret)) {
			t.Fatal("key material or plaintext leaked into output")
		}
	}
	gk, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := base64.StdEncoding.DecodeString(gk); err != nil || len(raw) != KeySize {
		t.Fatal("generated key invalid")
	}
}

func TestConcurrentUse(t *testing.T) {
	v := newVault(t)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			aad := []byte(fmt.Sprintf("subaccount:%d:fake", i))
			pt := []byte(fmt.Sprintf("fake-secret-%d", i))
			s, err := v.Seal(pt, aad)
			if err != nil {
				errs <- err
				return
			}
			got, err := v.Open(s, aad)
			if err != nil || !bytes.Equal(got, pt) {
				errs <- fmt.Errorf("goroutine %d roundtrip failed", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
