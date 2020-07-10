package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"time"

	"github.com/drakkan/sftpgo/logger"
	"github.com/drakkan/sftpgo/utils"
)

// Config defines the configuration for HTTP clients.
// HTTP clients are used for executing hooks such as the ones used for
// custom actions, external authentication and pre-login user modifications
type Config struct {
	// Timeout specifies a time limit, in seconds, for requests
	Timeout int64 `json:"timeout" mapstructure:"timeout"`
	// CACertificates defines extra CA certificates to trust.
	// The paths can be absolute or relative to the config dir.
	// Adding trusted CA certificates is a convenient way to use self-signed
	// certificates without defeating the purpose of using TLS
	CACertificates []string `json:"ca_certificates" mapstructure:"ca_certificates"`
	// if enabled the HTTP client accepts any TLS certificate presented by
	// the server and any host name in that certificate.
	// In this mode, TLS is susceptible to man-in-the-middle attacks.
	// This should be used only for testing.
	SkipTLSVerify   bool `json:"skip_tls_verify" mapstructure:"skip_tls_verify"`
	customTransport *http.Transport
}

const logSender = "httpclient"

var httpConfig Config

// Initialize configures HTTP clients
func (c Config) Initialize(configDir string) {
	httpConfig = c
	rootCAs := c.loadCACerts(configDir)
	customTransport := http.DefaultTransport.(*http.Transport).Clone()
	if customTransport.TLSClientConfig != nil {
		customTransport.TLSClientConfig.RootCAs = rootCAs
	} else {
		customTransport.TLSClientConfig = &tls.Config{
			RootCAs: rootCAs,
		}
	}
	customTransport.TLSClientConfig.InsecureSkipVerify = c.SkipTLSVerify
	httpConfig.customTransport = customTransport
}

// loadCACerts returns system cert pools and try to add the configured
// CA certificates to it
func (c Config) loadCACerts(configDir string) *x509.CertPool {
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		rootCAs = x509.NewCertPool()
	}

	for _, ca := range c.CACertificates {
		if !utils.IsFileInputValid(ca) {
			logger.Warn(logSender, "", "unable to load invalid CA certificate: %#v", ca)
			logger.WarnToConsole("unable to load invalid CA certificate: %#v", ca)
			continue
		}
		if !filepath.IsAbs(ca) {
			ca = filepath.Join(configDir, ca)
		}
		certs, err := ioutil.ReadFile(ca)
		if err != nil {
			logger.Warn(logSender, "", "unable to load CA certificate: %v", err)
			logger.WarnToConsole("unable to load CA certificate: %#v", err)
		}
		if rootCAs.AppendCertsFromPEM(certs) {
			logger.Debug(logSender, "", "CA certificate %#v added to the trusted certificates", ca)
		} else {
			logger.Warn(logSender, "", "unable to add CA certificate %#v to the trusted cetificates", ca)
			logger.WarnToConsole("unable to add CA certificate %#v to the trusted cetificates", ca)
		}
	}
	return rootCAs
}

// GetHTTPClient returns an HTTP client with the configured parameters
func GetHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   time.Duration(httpConfig.Timeout) * time.Second,
		Transport: httpConfig.customTransport,
	}
}
