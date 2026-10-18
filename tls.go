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
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/appital/gofix/config"
)

func loadTLSConfig(settings *SessionSettings) (*tls.Config, error) {
	var err error
	allowSkipClientCerts := false
	if settings.HasSetting(config.SocketUseSSL) {
		allowSkipClientCerts, err = settings.BoolSetting(config.SocketUseSSL)
		if err != nil {
			return nil, err
		}
	}

	var serverName string
	if settings.HasSetting(config.SocketServerName) {
		serverName, err = settings.Setting(config.SocketServerName)
		if err != nil {
			return nil, err
		}
	}

	insecureSkipVerify := false
	if settings.HasSetting(config.SocketInsecureSkipVerify) {
		insecureSkipVerify, err = settings.BoolSetting(config.SocketInsecureSkipVerify)
		if err != nil {
			return nil, err
		}
	}

	if !settings.HasSetting(config.SocketPrivateKeyFile) &&
		!settings.HasSetting(config.SocketCertificateFile) &&
		!settings.HasSetting(config.SocketPrivateKeyBytes) &&
		!settings.HasSetting(config.SocketCertificateBytes) {
		if !allowSkipClientCerts {
			return nil, nil
		}
	}

	tlsConfig := defaultTLSConfig()
	tlsConfig.ServerName = serverName
	tlsConfig.InsecureSkipVerify = insecureSkipVerify
	if err = setMinVersionExplicit(settings, tlsConfig); err != nil {
		return nil, err
	}

	if settings.HasSetting(config.SocketPrivateKeyFile) || settings.HasSetting(config.SocketCertificateFile) {

		var privateKeyFile string
		var certificateFile string

		privateKeyFile, err = settings.Setting(config.SocketPrivateKeyFile)
		if err != nil {
			return nil, err
		}

		certificateFile, err = settings.Setting(config.SocketCertificateFile)
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = make([]tls.Certificate, 1)

		if tlsConfig.Certificates[0], err = tls.LoadX509KeyPair(certificateFile, privateKeyFile); err != nil {
			return nil, fmt.Errorf("failed to load key pair: %w", err)
		}
	} else if settings.HasSetting(config.SocketPrivateKeyBytes) || settings.HasSetting(config.SocketCertificateBytes) {
		privateKeyBytes, err := settings.RawSetting(config.SocketPrivateKeyBytes)
		if err != nil {
			return nil, err
		}

		certificateBytes, err := settings.RawSetting(config.SocketCertificateBytes)
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = make([]tls.Certificate, 1)

		certificate, err := tls.X509KeyPair(certificateBytes, privateKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse key pair: %w", err)
		}

		tlsConfig.Certificates[0] = certificate
	}

	if !allowSkipClientCerts {
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	if !settings.HasSetting(config.SocketCAFile) && !settings.HasSetting(config.SocketCABytes) {
		return tlsConfig, nil
	}

	certPool := x509.NewCertPool()
	if settings.HasSetting(config.SocketCAFile) {
		caFile, err := settings.Setting(config.SocketCAFile)
		if err != nil {
			return nil, err
		}

		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA bundle: %w", err)
		}

		if !certPool.AppendCertsFromPEM(pem) {
			err = fmt.Errorf("failed to parse %v", caFile)
			return nil, err
		}
	} else {
		caBytes, err := settings.RawSetting(config.SocketCABytes)
		if err != nil {
			return nil, err
		}

		if !certPool.AppendCertsFromPEM(caBytes) {
			err = errors.New("failed to parse CA bundle from raw bytes")
			return nil, err
		}
	}

	tlsConfig.RootCAs = certPool
	tlsConfig.ClientCAs = certPool

	return tlsConfig, nil
}

func defaultTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
}

func setMinVersionExplicit(settings *SessionSettings, tlsConfig *tls.Config) error {
	if !settings.HasSetting(config.SocketMinimumTLSVersion) {
		return nil
	}

	minVersion, err := settings.Setting(config.SocketMinimumTLSVersion)
	if err != nil {
		return err
	}

	switch minVersion {
	case "TLS12":
		tlsConfig.MinVersion = tls.VersionTLS12
	case "TLS13":
		tlsConfig.MinVersion = tls.VersionTLS13
	default:
		return IncorrectFormatForSetting{Setting: config.SocketMinimumTLSVersion, Value: []byte(minVersion)}
	}

	return nil
}
