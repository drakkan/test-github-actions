package dataprovider

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPasswordCache(t *testing.T) {
	config.PasswordCaching = true
	username := "user"
	password := "pwd"
	cachedPasswords.Add(username, "")
	found, match := CheckCachedPassword(username, password)
	assert.False(t, found)
	assert.False(t, match)

	cachedPasswords.Add(username, password)
	found, match = CheckCachedPassword(username, password)
	assert.True(t, found)
	assert.True(t, match)

	found, match = CheckCachedPassword(username, password+"_")
	assert.True(t, found)
	assert.False(t, match)

	found, match = CheckCachedPassword("", password)
	assert.False(t, found)
	assert.False(t, match)

	config.PasswordCaching = false
	cachedPasswords.Remove(username)
	found, match = CheckCachedPassword(username, password)
	assert.True(t, found)
	assert.True(t, match)

	config.PasswordCaching = true

	cachedPasswords.Remove(username)
	found, match = CheckCachedPassword(username, password)
	assert.False(t, found)
	assert.False(t, match)
}
