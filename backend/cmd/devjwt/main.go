// Command devjwt mints an access token for calling the API directly.
//
// It mints through services.TokenClaims itself, so a field that is added,
// renamed or made mandatory changes this tool at COMPILE time rather than
// producing a token the server silently rejects with "invalid token".
//
// It is never in a shipped image: the Dockerfile builds ./cmd/server.
//
// Usage — always ON the server, so the secret never travels:
//
//	docker exec orchestrator-backend cat /run/secrets/jwt_secret   # or $JWT_SECRET_KEY
//	go run ./cmd/devjwt -secret "$SECRET" -user <uuid> -account <uuid>
//
// The ids must be real; the middleware accepts any signed token, but every
// handler resolves them:
//
//	select user_id, account_id from account_members where status = 'active' limit 1;
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

func main() {
	secret := flag.String("secret", "", "JWT signing secret; where JWT_SECRET_KEY_FILE is set this is the CONTENTS OF THAT FILE: the server then never reads the plain JWT_SECRET_KEY variable, whatever it holds")
	userID := flag.String("user", "", "user id (uuid)")
	accountID := flag.String("account", "", "account id (uuid)")
	email := flag.String("email", "dev@example.com", "email claim; informational")
	issuer := flag.String("issuer", "orchestrator", "issuer claim; must match the server's jwt.issuer only if it ever starts validating it")
	ttl := flag.Duration("ttl", 15*time.Minute, "how long the token is valid")
	flag.Parse()

	if *secret == "" || *userID == "" || *accountID == "" {
		fmt.Fprintln(os.Stderr, "usage: devjwt -secret <s> -user <uuid> -account <uuid> [-ttl 15m]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	uid, err := uuid.Parse(*userID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -user: %v\n", err)
		os.Exit(2)
	}
	aid, err := uuid.Parse(*accountID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -account: %v\n", err)
		os.Exit(2)
	}

	now := time.Now()
	// TokenTypeAccess is not optional: the middleware refuses refresh and
	// pre-auth tokens with "invalid token type", which reads like a bad secret.
	claims := &services.TokenClaims{
		UserID:    uid,
		AccountID: aid,
		Email:     *email,
		TokenType: services.TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    *issuer,
			Subject:   uid.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(*ttl)),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(*secret))
	if err != nil {
		fmt.Fprintf(os.Stderr, "signing failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(signed)
}
