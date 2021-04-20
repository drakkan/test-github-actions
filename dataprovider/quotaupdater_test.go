package dataprovider

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/drakkan/sftpgo/vfs"
)

//nolint:dupl
func TestUserQuotaUpdater(t *testing.T) {
	user1 := "user1"
	q := newQuotaUpdater()
	q.updateUserQuota(user1, 10, 1234)
	files, size := q.getUserPendingQuota(user1)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(1234), size)
	assert.Len(t, q.getUsernames(), 1)

	q.updateUserQuota(user1, -10, -1234)
	files, size = q.getUserPendingQuota(user1)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)
	assert.Len(t, q.getUsernames(), 0)

	q.updateUserQuota(user1, 10, 1234)
	files, size = q.getUserPendingQuota(user1)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(1234), size)
	assert.Len(t, q.getUsernames(), 1)

	q.resetUserQuota(user1)
	files, size = q.getUserPendingQuota(user1)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)
	assert.Len(t, q.getUsernames(), 0)
}

//nolint:dupl
func TestFolderQuotaUpdater(t *testing.T) {
	folder1 := "folder1"
	q := newQuotaUpdater()
	q.updateFolderQuota(folder1, 10, 1234)
	files, size := q.getFolderPendingQuota(folder1)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(1234), size)
	assert.Len(t, q.getFoldernames(), 1)

	q.updateFolderQuota(folder1, -10, -1234)
	files, size = q.getFolderPendingQuota(folder1)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)
	assert.Len(t, q.getFoldernames(), 0)

	q.updateFolderQuota(folder1, 10, 1234)
	files, size = q.getFolderPendingQuota(folder1)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(1234), size)
	assert.Len(t, q.getFoldernames(), 1)

	q.resetFolderQuota(folder1)
	files, size = q.getFolderPendingQuota(folder1)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)
	assert.Len(t, q.getFoldernames(), 0)
}

func TestQuotaUpdater(t *testing.T) {
	c := Config{
		Driver:          "sqlite",
		Name:            "sftpgo.db",
		TrackQuota:      1,
		CredentialsPath: "credentials",
		PasswordHashing: PasswordHashing{
			Argon2Options: Argon2Options{
				Memory:      65536,
				Iterations:  1,
				Parallelism: 2,
			},
		},
		DelayedQuotaUpdate: 1,
	}

	err := Initialize(c, "..", false)
	assert.NoError(t, err)
	// wait for start
	time.Sleep(100 * time.Millisecond)
	delayedQuotaUpdater.setWaitTime(0)
	// wait for exit
	time.Sleep(1200 * time.Millisecond)

	user := getTestUser()
	err = AddUser(&user)
	assert.NoError(t, err)

	err = UpdateUserQuota(&user, 10, 6000, false)
	assert.NoError(t, err)
	files, size := delayedQuotaUpdater.getUserPendingQuota(user.Username)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)
	files, size, err = GetUsedQuota(user.Username)
	assert.NoError(t, err)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)

	delayedQuotaUpdater.storeUsersQuota()
	files, size, err = GetUsedQuota(user.Username)
	assert.NoError(t, err)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)
	files, size = delayedQuotaUpdater.getUserPendingQuota(user.Username)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)

	folder := vfs.BaseVirtualFolder{
		Name:       "folder",
		MappedPath: filepath.Join(os.TempDir(), "p"),
	}
	err = AddFolder(&folder)
	assert.NoError(t, err)

	err = UpdateVirtualFolderQuota(&folder, 10, 6000, false)
	assert.NoError(t, err)
	files, size = delayedQuotaUpdater.getFolderPendingQuota(folder.Name)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)
	files, size, err = GetUsedVirtualFolderQuota(folder.Name)
	assert.NoError(t, err)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)

	delayedQuotaUpdater.storeFoldersQuota()
	files, size, err = GetUsedVirtualFolderQuota(folder.Name)
	assert.NoError(t, err)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)
	files, size = delayedQuotaUpdater.getFolderPendingQuota(user.Username)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)

	err = Close()
	assert.NoError(t, err)

	err = UpdateUserQuota(&user, 10, 6000, false)
	assert.NoError(t, err)
	err = UpdateVirtualFolderQuota(&folder, 10, 6000, false)
	assert.NoError(t, err)
	_, _, err = GetUsedQuota(user.Username)
	assert.Error(t, err)
	_, _, err = GetUsedVirtualFolderQuota(folder.Name)
	assert.Error(t, err)

	delayedQuotaUpdater.storeUsersQuota()
	delayedQuotaUpdater.storeFoldersQuota()
	files, size = delayedQuotaUpdater.getUserPendingQuota(user.Username)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)
	files, size = delayedQuotaUpdater.getFolderPendingQuota(folder.Name)
	assert.Equal(t, 10, files)
	assert.Equal(t, int64(6000), size)

	c.DelayedQuotaUpdate = 0
	err = Initialize(c, "..", false)
	assert.NoError(t, err)

	delayedQuotaUpdater.storeUsersQuota()
	delayedQuotaUpdater.storeFoldersQuota()
	files, size = delayedQuotaUpdater.getUserPendingQuota(user.Username)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)
	files, size = delayedQuotaUpdater.getFolderPendingQuota(folder.Name)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), size)

	files, size, err = GetUsedQuota(user.Username)
	assert.NoError(t, err)
	assert.Equal(t, 10*2, files)
	assert.Equal(t, int64(6000)*2, size)
	files, size, err = GetUsedVirtualFolderQuota(folder.Name)
	assert.NoError(t, err)
	assert.Equal(t, 10*2, files)
	assert.Equal(t, int64(6000)*2, size)

	err = DeleteUser(user.Username)
	assert.NoError(t, err)

	err = DeleteFolder(folder.Name)
	assert.NoError(t, err)
}

func getTestUser() User {
	username := "user"
	password := "password"
	user := User{
		Username:    username,
		Password:    password,
		HomeDir:     filepath.Join(os.TempDir(), username),
		Status:      1,
		Description: "test user",
		QuotaFiles:  100,
	}
	user.Permissions = make(map[string][]string)
	user.Permissions["/"] = []string{"*"}
	return user
}
