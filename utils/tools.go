package utils

import (
	"crypto/rand"
	"encoding/json"
)

func EncodeToJSONString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func GenerateRandomString(length int) string {
	// with a-z, A-Z, 0-9 and special characters, total 62 characters
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()-+=~`[]{}|;:,.<>/?"
	b := make([]byte, length)
	rand.Read(b)
	for i := 0; i < length; i++ {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}
