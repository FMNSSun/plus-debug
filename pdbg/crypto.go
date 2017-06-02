package pdbg

import "crypto/sha256"
import "bytes"


type CryptoContext struct {
	key		[]byte
	secret  []byte
}

func (c *CryptoContext) encrypt(data []byte) {
	keyLen := len(c.key)
	dataLen := len(data)

	for i := 0; i < dataLen; i++ {
		data[i] ^= c.key[i % keyLen]
	}
}

func (c *CryptoContext) decrypt(data []byte) {
	// Luckily our stupid encryption is an involution.
	c.encrypt(data)
}

func (c *CryptoContext) EncryptAndProtect(plusHeader []byte, payload []byte) ([]byte, error) {
	buf := make([]byte, (17 + len(plusHeader) + len(payload))) // reserve 16 bytes for checksum + 1 byte for header len
	bufStart := buf[17:] // 0-15 is for checksum/secret afterwards, 16 for header len
	buf[16] = byte(len(plusHeader))
	_ = copy(bufStart, plusHeader)
	_ = copy(bufStart[:len(plusHeader)], payload)

	_ = copy(buf, c.secret)

	c.encrypt(buf[16:])

	hash := sha256.Sum256(buf)

	if len(hash) != 16 {
		panic("Hash has bogus length! BUG. REPORT THIS!")
	}

	_ = copy(buf, hash[0:])

	return buf, nil
}

func (c *CryptoContext) DecryptAndValidate(plusHeader []byte, payload []byte) ([]byte, bool, error) {
	// Save the hash for comparison
	packetHash := make([]byte, 16)
	_ = copy(packetHash, payload[0:16])

	// Set the secret
	_ = copy(payload, c.secret)

	// Compute the hash
	hash_ := sha256.Sum256(payload)
	hash := hash_[0:]

	// If hashes are not equal something is fishy
	if !bytes.Equal(packetHash, hash) {
		return payload, false, nil
	}

	// Decrypt the packet
	c.decrypt(payload[16:])

	headerLen := payload[16]

	// Extract the header
	header := payload[17:17+headerLen]

	// Compare it with the header we got
	if !bytes.Equal(plusHeader, header) {
		return payload, false, nil
	}

	payload = payload[17+headerLen:]

	return payload, true, nil
}
