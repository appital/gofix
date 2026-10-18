// Copyright (c) quickfixengine.org  All rights reserved.
//
// This file may be distributed under the terms of the quickfixengine.org
// license as defined by quickfixengine.org and appearing in the file
// LICENSE included in the packaging of this file.
//
// This file is provided AS IS with NO WARRANTY OF ANY KIND, INCLUDING
// THE WARRANTY OF DESIGN, MERCHANTABILITY AND FITNESS FOR A
// PARTICULAR PURPOSE.
//
// See http://www.quickfixengine.org/LICENSE for licensing information.
//
// Contact ask@quickfixengine.org if any conditions of this licensing
// are not clear to you.

package gofix

import (
	"crypto/tls"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/appital/gofix/config"
)

type TLSTestSuite struct {
	suite.Suite
	settings                                   *Settings
	PrivateKeyFile, CertificateFile, CAFile    string
	PrivateKeyBytes, CertificateBytes, CABytes []byte
}

func TestTLSTestSuite(t *testing.T) {
	suite.Run(t, new(TLSTestSuite))
}

func (s *TLSTestSuite) SetupTest() {
	s.settings = NewSettings()
	s.PrivateKeyFile = "_test_data/localhost.key"
	s.CertificateFile = "_test_data/localhost.crt"
	s.CAFile = "_test_data/ca.crt"

	privateKeyBytes, err := os.ReadFile(s.PrivateKeyFile)
	s.Require().NoError(err)
	s.PrivateKeyBytes = privateKeyBytes

	certificateBytes, err := os.ReadFile(s.CertificateFile)
	s.Require().NoError(err)
	s.CertificateBytes = certificateBytes

	caBytes, err := os.ReadFile(s.CAFile)
	s.Require().NoError(err)
	s.CABytes = caBytes
}

func (s *TLSTestSuite) TestLoadTLSNoSettings() {
	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(tlsConfig)
	s.Nil(err)
}

func (s *TLSTestSuite) TestLoadTLSMissingKeyOrCert() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	_, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "Conditionally Required Setting: SocketCertificateFile")

	s.SetupTest()
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)
	_, err = loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "Conditionally Required Setting: SocketPrivateKeyFile")
}

func (s *TLSTestSuite) TestLoadTLSInvalidKeyOrCert() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, "blah")
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, "foo")
	_, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "failed to load key pair: open foo: no such file or directory")
}

func (s *TLSTestSuite) TestLoadTLSNoCA() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.Len(tlsConfig.Certificates, 1)
	s.Nil(tlsConfig.RootCAs)
	s.Nil(tlsConfig.ClientCAs)
	s.Equal(tls.RequireAndVerifyClientCert, tlsConfig.ClientAuth)
}

func (s *TLSTestSuite) TestLoadTLSWithBadCA() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)
	s.settings.GlobalSettings().Set(config.SocketCAFile, "bar")

	_, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "failed to read CA bundle: open bar: no such file or directory")
}

func (s *TLSTestSuite) TestLoadTLSWithCA() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)
	s.settings.GlobalSettings().Set(config.SocketCAFile, s.CAFile)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.Len(tlsConfig.Certificates, 1)
	s.NotNil(tlsConfig.RootCAs)
	s.NotNil(tlsConfig.ClientCAs)
	s.Equal(tls.RequireAndVerifyClientCert, tlsConfig.ClientAuth)
}

func (s *TLSTestSuite) TestLoadTLSWithOnlyCA() {
	s.settings.GlobalSettings().Set(config.SocketUseSSL, "Y")
	s.settings.GlobalSettings().Set(config.SocketCAFile, s.CAFile)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.NotNil(tlsConfig.RootCAs)
	s.NotNil(tlsConfig.ClientCAs)
}

func (s *TLSTestSuite) TestLoadTLSWithoutSSLWithOnlyCA() {
	s.settings.GlobalSettings().Set(config.SocketCAFile, s.CAFile)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.Nil(tlsConfig)
}

func (s *TLSTestSuite) TestLoadTLSAllowSkipClientCerts() {
	s.settings.GlobalSettings().Set(config.SocketUseSSL, "Y")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.Equal(tls.NoClientCert, tlsConfig.ClientAuth)
}

func (s *TLSTestSuite) TestServerNameUseSSL() {
	s.settings.GlobalSettings().Set(config.SocketUseSSL, "Y")
	s.settings.GlobalSettings().Set(config.SocketServerName, "DummyServerNameUseSSL")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)
	s.Equal("DummyServerNameUseSSL", tlsConfig.ServerName)
}

func (s *TLSTestSuite) TestServerNameWithCerts() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)
	s.settings.GlobalSettings().Set(config.SocketServerName, "DummyServerNameWithCerts")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)
	s.Equal("DummyServerNameWithCerts", tlsConfig.ServerName)
}

func (s *TLSTestSuite) TestInsecureSkipVerify() {
	s.settings.GlobalSettings().Set(config.SocketInsecureSkipVerify, "Y")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.Nil(tlsConfig)
}

func (s *TLSTestSuite) TestInsecureSkipVerifyWithUseSSL() {
	s.settings.GlobalSettings().Set(config.SocketUseSSL, "Y")
	s.settings.GlobalSettings().Set(config.SocketInsecureSkipVerify, "Y")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.True(tlsConfig.InsecureSkipVerify)
}

func (s *TLSTestSuite) TestInsecureSkipVerifyAndCerts() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)
	s.settings.GlobalSettings().Set(config.SocketInsecureSkipVerify, "Y")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.True(tlsConfig.InsecureSkipVerify)
	s.Len(tlsConfig.Certificates, 1)
}

func (s *TLSTestSuite) TestDefaultTLSConfig() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)
	s.Equal(uint16(tls.VersionTLS12), tlsConfig.MinVersion)
	s.Nil(tlsConfig.CurvePreferences)
}

func (s *TLSTestSuite) TestMinimumTLSVersion() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)

	// TLS12
	s.settings.GlobalSettings().Set(config.SocketMinimumTLSVersion, "TLS12")
	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())

	s.Nil(err)
	s.NotNil(tlsConfig)
	s.Equal(uint16(tls.VersionTLS12), tlsConfig.MinVersion)

	// TLS13
	s.settings.GlobalSettings().Set(config.SocketMinimumTLSVersion, "TLS13")
	tlsConfig, err = loadTLSConfig(s.settings.GlobalSettings())

	s.Nil(err)
	s.NotNil(tlsConfig)
	s.Equal(uint16(tls.VersionTLS13), tlsConfig.MinVersion)
}

func (s *TLSTestSuite) TestMinimumTLSVersionRejectsDeprecatedVersions() {
	s.settings.GlobalSettings().Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	s.settings.GlobalSettings().Set(config.SocketCertificateFile, s.CertificateFile)

	for _, version := range []string{"SSL30", "TLS10", "TLS11", "bogus"} {
		s.settings.GlobalSettings().Set(config.SocketMinimumTLSVersion, version)
		_, err := loadTLSConfig(s.settings.GlobalSettings())
		s.EqualError(err, `"`+version+`" is invalid for SocketMinimumTLSVersion`)
	}
}

func (s *TLSTestSuite) TestVerifiedHandshake() {
	for _, version := range []string{"TLS12", "TLS13"} {
		s.Run(version, func() {
			acceptorSettings := NewSessionSettings()
			acceptorSettings.Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
			acceptorSettings.Set(config.SocketCertificateFile, s.CertificateFile)
			acceptorSettings.Set(config.SocketCAFile, s.CAFile)
			acceptorSettings.Set(config.SocketMinimumTLSVersion, version)
			acceptorConfig, err := loadTLSConfig(acceptorSettings)
			s.Require().NoError(err)

			initiatorSettings := NewSessionSettings()
			initiatorSettings.Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
			initiatorSettings.Set(config.SocketCertificateFile, s.CertificateFile)
			initiatorSettings.Set(config.SocketCAFile, s.CAFile)
			initiatorSettings.Set(config.SocketServerName, "localhost")
			initiatorSettings.Set(config.SocketMinimumTLSVersion, version)
			initiatorConfig, err := loadTLSConfig(initiatorSettings)
			s.Require().NoError(err)

			listener, err := tls.Listen("tcp", "127.0.0.1:0", acceptorConfig)
			s.Require().NoError(err)
			defer listener.Close()

			acceptErr := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					acceptErr <- err
					return
				}
				defer conn.Close()
				acceptErr <- conn.(*tls.Conn).Handshake()
			}()

			conn, err := tls.Dial("tcp", listener.Addr().String(), initiatorConfig)
			s.Require().NoError(err)
			defer conn.Close()
			s.Require().NoError(<-acceptErr)

			state := conn.ConnectionState()
			s.GreaterOrEqual(state.Version, acceptorConfig.MinVersion)
			s.Len(state.VerifiedChains, 1)
		})
	}
}

func (s *TLSTestSuite) TestVerifiedHandshakeRejectsClientWithoutCert() {
	acceptorSettings := NewSessionSettings()
	acceptorSettings.Set(config.SocketPrivateKeyFile, s.PrivateKeyFile)
	acceptorSettings.Set(config.SocketCertificateFile, s.CertificateFile)
	acceptorSettings.Set(config.SocketCAFile, s.CAFile)
	acceptorConfig, err := loadTLSConfig(acceptorSettings)
	s.Require().NoError(err)

	initiatorSettings := NewSessionSettings()
	initiatorSettings.Set(config.SocketUseSSL, "Y")
	initiatorSettings.Set(config.SocketCAFile, s.CAFile)
	initiatorSettings.Set(config.SocketServerName, "localhost")
	initiatorConfig, err := loadTLSConfig(initiatorSettings)
	s.Require().NoError(err)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", acceptorConfig)
	s.Require().NoError(err)
	defer listener.Close()

	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		defer conn.Close()
		acceptErr <- conn.(*tls.Conn).Handshake()
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	s.Require().NoError(err)
	client := tls.Client(conn, initiatorConfig)
	defer client.Close()
	_ = client.Handshake()

	s.ErrorContains(<-acceptErr, "client didn't provide a certificate")
}

func (s *TLSTestSuite) TestLoadTLSBytesMissingKeyOrCert() {
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, s.PrivateKeyBytes)
	_, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "Conditionally Required Setting: SocketCertificateBytes")

	s.SetupTest()
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, s.CertificateBytes)
	_, err = loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "Conditionally Required Setting: SocketPrivateKeyBytes")
}

func (s *TLSTestSuite) TestLoadTLSBytesInvalidKeyOrCert() {
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, s.PrivateKeyBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, []byte("not a cert"))
	_, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "failed to parse key pair: tls: failed to find any PEM data in certificate input")

	s.SetupTest()
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, s.CertificateBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, []byte("not a key"))
	_, err = loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "failed to parse key pair: tls: failed to find any PEM data in key input")
}

func (s *TLSTestSuite) TestLoadTLSBytesNoCA() {
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, s.PrivateKeyBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, s.CertificateBytes)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NoError(err)
	s.NotNil(tlsConfig)

	s.Len(tlsConfig.Certificates, 1)
	s.Nil(tlsConfig.RootCAs)
	s.Nil(tlsConfig.ClientCAs)
	s.Equal(tls.RequireAndVerifyClientCert, tlsConfig.ClientAuth)
}

func (s *TLSTestSuite) TestLoadTLSBytesWithBadCA() {
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, s.PrivateKeyBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, s.CertificateBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCABytes, []byte("bar"))

	_, err := loadTLSConfig(s.settings.GlobalSettings())
	s.NotNil(err)
	s.EqualError(err, "failed to parse CA bundle from raw bytes")
}

func (s *TLSTestSuite) TestLoadTLSBytesWithCA() {
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, s.PrivateKeyBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, s.CertificateBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCABytes, s.CABytes)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.Len(tlsConfig.Certificates, 1)
	s.NotNil(tlsConfig.RootCAs)
	s.NotNil(tlsConfig.ClientCAs)
	s.Equal(tls.RequireAndVerifyClientCert, tlsConfig.ClientAuth)
}

func (s *TLSTestSuite) TestLoadTLSBytesWithOnlyCA() {
	s.settings.GlobalSettings().Set(config.SocketUseSSL, "Y")
	s.settings.GlobalSettings().SetRaw(config.SocketCABytes, s.CABytes)

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)

	s.NotNil(tlsConfig.RootCAs)
	s.NotNil(tlsConfig.ClientCAs)
}

func (s *TLSTestSuite) TestServerNameWithCertsFromBytes() {
	s.settings.GlobalSettings().SetRaw(config.SocketPrivateKeyBytes, s.PrivateKeyBytes)
	s.settings.GlobalSettings().SetRaw(config.SocketCertificateBytes, s.CertificateBytes)
	s.settings.GlobalSettings().Set(config.SocketServerName, "DummyServerNameWithCerts")

	tlsConfig, err := loadTLSConfig(s.settings.GlobalSettings())
	s.Nil(err)
	s.NotNil(tlsConfig)
	s.Equal("DummyServerNameWithCerts", tlsConfig.ServerName)
}
