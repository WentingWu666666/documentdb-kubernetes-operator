// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package otel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpreview "github.com/documentdb/documentdb-operator/api/preview"
	util "github.com/documentdb/documentdb-operator/internal/utils"
)

func testCertPEM(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func useSystemBundle(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := systemCABundlePath
	systemCABundlePath = path
	t.Cleanup(func() { systemCABundlePath = old })
}

func TestBuildProxyCABundle(t *testing.T) {
	system := testCertPEM(t, "system-root")
	proxy := testCertPEM(t, "proxy-ca")
	useSystemBundle(t, system)

	got, err := BuildProxyCABundle(proxy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, strings.TrimSpace(system)) || !strings.HasSuffix(got, proxy) {
		t.Errorf("bundle should be system roots followed by the proxy CA, got:\n%s", got)
	}

	if _, err := BuildProxyCABundle("not a cert"); err == nil {
		t.Error("expected error for input without a certificate")
	}
	bad := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	if _, err := BuildProxyCABundle(bad); err == nil {
		t.Error("expected error for a malformed certificate")
	}

	systemCABundlePath = filepath.Join(t.TempDir(), "missing")
	if _, err := BuildProxyCABundle(proxy); err == nil {
		t.Error("expected error when the system bundle is unreadable")
	}
}

func TestGenerateConfigMapDataProxyCA(t *testing.T) {
	useSystemBundle(t, testCertPEM(t, "system-root"))

	spec := &dbpreview.MonitoringSpec{
		Enabled:  true,
		Exporter: &dbpreview.ExporterSpec{Prometheus: &dbpreview.PrometheusExporterSpec{Port: 9090}},
	}
	t.Setenv(util.PROXY_CERT_ENV, "")
	data, err := GenerateConfigMapData("c", "ns", spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data[ProxyCABundleKey]; ok {
		t.Error("CA bundle must be absent when no proxy cert is configured")
	}
	before := HashConfigMapData(data)

	t.Setenv(util.PROXY_CERT_ENV, testCertPEM(t, "proxy-ca"))
	data, err = GenerateConfigMapData("c", "ns", spec)
	if err != nil {
		t.Fatal(err)
	}
	if data[ProxyCABundleKey] == "" {
		t.Fatal("CA bundle missing when proxy cert is configured")
	}
	if HashConfigMapData(data) == before {
		t.Error("config hash must change when the proxy CA is added so pods restart")
	}
}
