package main

// Account erasure plumbing (card openpak/prds#120): the HTTP handler for
// DELETE /internal/accounts/{account_id}/data and the read-only core client that lists an account's
// Switch links (namespace "switch", subject = BAAS user id), which is how the vendor ids this
// service stores are tied back to the OpenPak account.

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var errNoCore = errors.New("account core not configured (ACCOUNT_GRPC_ADDR, ACCOUNT_INTERNAL_KEY)")

// switchLinks lists an account's Switch link subjects (BAAS user ids).
type switchLinks interface {
	SwitchSubjects(ctx context.Context, account string) ([]string, error)
}

type grpcLinks struct{ links accountv1.LinksClient }

// newLinksFromEnv dials the account core, or returns nil when it is not configured.
func newLinksFromEnv() switchLinks {
	addr, key := os.Getenv("ACCOUNT_GRPC_ADDR"), os.Getenv("ACCOUNT_INTERNAL_KEY")
	if addr == "" || key == "" {
		return nil
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(ctx, "x-internal-key", key), 5*time.Second)
			defer cancel()
			return invoker(ctx, method, req, reply, cc, opts...)
		}))
	if err != nil {
		log.Printf("ERROR account core: %v", err)
		return nil
	}
	return &grpcLinks{accountv1.NewLinksClient(conn)}
}

func (c *grpcLinks) SwitchSubjects(ctx context.Context, account string) ([]string, error) {
	r, err := c.links.ListAccountLinks(ctx, &accountv1.ListAccountLinksRequest{AccountId: account, Namespace: "switch"})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range r.GetLinks() {
		if l.GetSubjectId() != "" {
			out = append(out, l.GetSubjectId())
		}
	}
	return out, nil
}

// eraseHandler serves DELETE /internal/accounts/{account_id}/data with Bearer ACCOUNT_ERASE_KEY.
// The key is read per request; unset means the endpoint answers 503, never open.
func eraseHandler(service string, erase func(ctx context.Context, account string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := os.Getenv("ACCOUNT_ERASE_KEY")
		if key == "" {
			http.Error(w, "erase not configured", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(key)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/accounts/"), "/data")
		if !uuidRE.MatchString(id) {
			http.Error(w, "bad account id", http.StatusBadRequest)
			return
		}
		id = strings.ToLower(id)
		n, err := erase(r.Context(), id)
		if err != nil {
			log.Printf("ERROR %s erase failed: %v", service, err)
			http.Error(w, "erase failed", http.StatusInternalServerError)
			return
		}
		log.Printf("account erased account=%s rows=%d service=%s", id, n, service)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"` + service + `","rows":` + itoaErase(n) + `}` + "\n"))
	}
}

func itoaErase(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}
