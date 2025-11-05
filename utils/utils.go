package utils

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"
)

// // logError logs errors with a standard format.
// func logError(err error) {
// 	if err != nil {
// 		log.Printf("[ERROR] %v", err)
// 	}
// }

// // itoa converts an int to string.
// func itoa(i int) string {
// 	return strconv.Itoa(i)
// }

// var globalConnID uint32

// // Previously used for tcp multiplexing
// func nextID() uint32 {
// 	return atomic.AddUint32(&globalConnID, 1)
// }

const pbkdf2Iterations = 250000

func GenerateSelfSignedCert() tls.Certificate {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Salmon Cannon"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, _ := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	certPEM := pemEncode("CERTIFICATE", derBytes)
	keyPEM := pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(priv))
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	return cert
}

func pemEncode(typ string, data []byte) []byte {
	var buf bytes.Buffer
	pem.Encode(&buf, &pem.Block{Type: typ, Bytes: data})
	return buf.Bytes()
}

func DeriveEncKeyFromBytesAndSalt(sharedSecret string, salt []byte) ([]byte, error) {
	dk, err := pbkdf2.Key(sha512.New, sharedSecret, salt, pbkdf2Iterations, 32)
	if err != nil {
		return nil, err
	}
	return dk, nil

}
