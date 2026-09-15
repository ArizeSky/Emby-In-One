package backend

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hashed, err := HashPassword("secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if ok := VerifyPassword("secret", hashed); !ok {
		t.Fatalf("VerifyPassword returned false for matching password")
	}
	if ok := VerifyPassword("wrong", hashed); ok {
		t.Fatalf("VerifyPassword returned true for wrong password")
	}
}

func TestVerifyPasswordRejectsPlaintext(t *testing.T) {
	// Plaintext stored password must always be rejected, even if it matches input
	if VerifyPassword("secret", "secret") {
		t.Fatal("VerifyPassword should reject plaintext stored password")
	}
	if VerifyPassword("", "") {
		t.Fatal("VerifyPassword should reject empty inputs")
	}
	if VerifyPassword("x", "") {
		t.Fatal("VerifyPassword should reject empty stored")
	}
	if VerifyPassword("", "x") {
		t.Fatal("VerifyPassword should reject empty plain")
	}
}

// TestValidateTokenConcurrentWithTokenChanges hammers the unknown-token path while
// the token map is being written, so `go test -race` catches an unsynchronized map
// read. The writer mutates the map under the lock directly instead of calling
// Authenticate, whose scrypt hash would make this test needlessly slow.
func TestValidateTokenConcurrentWithTokenChanges(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		const iterations = 2000
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if info := app.Auth.ValidateToken("unknown-" + strconv.Itoa(i)); info != nil {
					t.Errorf("unknown token resolved to %+v", info)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				app.Auth.mu.Lock()
				app.Auth.tokens["issued-"+strconv.Itoa(i)] = tokenInfo{UserID: "user-id", Role: "admin"}
				app.Auth.mu.Unlock()
			}
		}()
		wg.Wait()
	})
}
