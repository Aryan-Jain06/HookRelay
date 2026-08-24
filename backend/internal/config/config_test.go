package config

import (
	"strings"
	"testing"
)

func TestValidateProductionAllowsDevelopmentDefaults(t *testing.T) {
	t.Parallel()
	// Outside production the guard must stay out of the way, or local
	// development breaks for everyone.
	c := &Config{
		Environment:           "development",
		JWTSecret:             DefaultJWTSecret,
		CORSAllowOrigin:       "*",
		AllowPrivateEndpoints: true,
	}
	if err := c.ValidateProduction(); err != nil {
		t.Fatalf("development config rejected: %v", err)
	}
}

func TestValidateProductionRejectsUnsafeConfig(t *testing.T) {
	t.Parallel()
	c := &Config{
		Environment:           EnvProduction,
		JWTSecret:             DefaultJWTSecret,
		CORSAllowOrigin:       "*",
		AllowPrivateEndpoints: true,
	}
	err := c.ValidateProduction()
	if err == nil {
		t.Fatal("unsafe production config accepted")
	}
	// Every problem should be reported at once, not one per restart.
	for _, want := range []string{"JWT_SECRET", "CORS_ALLOW_ORIGIN", "ALLOW_PRIVATE_ENDPOINTS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestValidateProductionAcceptsSafeConfig(t *testing.T) {
	t.Parallel()
	c := &Config{
		Environment:           EnvProduction,
		JWTSecret:             strings.Repeat("k", 48),
		CORSAllowOrigin:       "https://hookrelay.example.com",
		AllowPrivateEndpoints: false,
	}
	if err := c.ValidateProduction(); err != nil {
		t.Fatalf("safe production config rejected: %v", err)
	}
}

func TestValidateProductionRejectsShortSecret(t *testing.T) {
	t.Parallel()
	c := &Config{
		Environment:     EnvProduction,
		JWTSecret:       "short-but-not-the-default",
		CORSAllowOrigin: "https://hookrelay.example.com",
	}
	err := c.ValidateProduction()
	if err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short secret accepted or misreported: %v", err)
	}
}
