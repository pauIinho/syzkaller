// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package robustio

import (
	"io/ioutil"
	"math/rand"
	"os"
	"syscall"
	"time"
)

const arbitraryTimeout = 500 * time.Millisecond

const ERROR_SHARING_VIOLATION = 32

// retry retries ephemeral errors from f up to an arbitrary timeout
// to work around spurious filesystem errors on Windows
func retry(f func() (err error, mayRetry bool)) error {
	var (
		bestErr     error
		lowestErrno syscall.Errno
		start       time.Time
		nextSleep   time.Duration = 1 * time.Millisecond
	)
	for {
		err, mayRetry := f()
		if err == nil || !mayRetry {
			return err
		}

		if errno, ok := err.(syscall.Errno); ok && (lowestErrno == 0 || errno < lowestErrno) {
			bestErr = err
			lowestErrno = errno
		} else if bestErr == nil {
			bestErr = err
		}

		if start.IsZero() {
			start = time.Now()
		} else if d := time.Since(start) + nextSleep; d >= arbitraryTimeout {
			break
		}
		time.Sleep(nextSleep)
		nextSleep += time.Duration(rand.Int63n(int64(nextSleep)))
	}

	return bestErr
}

// rename is like os.Rename, but retries ephemeral errors.
//
// It wraps os.Rename, which (as of 2019-06-04) uses MoveFileEx with
// MOVEFILE_REPLACE_EXISTING.
//
// Windows also provides a different system call, ReplaceFile,
// that provides similar semantics, but perhaps preserves more metadata. (The
// documentation on the differences between the two is very sparse.)
//
// Empirical error rates with MoveFileEx are lower under modest concurrency, so
// for now we're sticking with what the os package already provides.
func rename(oldpath, newpath string) (err error) {
	return retry(func() (err error, mayRetry bool) {
		err = os.Rename(oldpath, newpath)
		return err, isEphemeralError(err)
	})
}

// readFile is like ioutil.ReadFile, but retries ephemeral errors.
func readFile(filename string) ([]byte, error) {
	var b []byte
	err := retry(func() (err error, mayRetry bool) {
		b, err = ioutil.ReadFile(filename)

		// Unlike in rename, we do not retry ERROR_FILE_NOT_FOUND here: it can occur
		// as a spurious error, but the file may also genuinely not exist, so the
		// increase in robustness is probably not worth the extra latency.

		return err, isEphemeralError(err) && err != syscall.ERROR_FILE_NOT_FOUND
	})
	return b, err
}

func removeAll(path string) error {
	return retry(func() (err error, mayRetry bool) {
		err = os.RemoveAll(path)
		return err, isEphemeralError(err)
	})
}

// isEphemeralError returns true if err may be resolved by waiting.
func isEphemeralError(err error) bool {
	switch werr := err.(type) {
	case *os.PathError:
		err = werr.Err
	case *os.LinkError:
		err = werr.Err
	case *os.SyscallError:
		err = werr.Err
	}
	if errno, ok := err.(syscall.Errno); ok {
		switch errno {
		case syscall.ERROR_ACCESS_DENIED,
			syscall.ERROR_FILE_NOT_FOUND,
			ERROR_SHARING_VIOLATION:
			return true
		}
	}
	return false
}