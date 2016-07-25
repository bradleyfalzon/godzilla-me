package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"

	"github.com/boltdb/bolt"
)

// boltInitalise initialises the bolt database
func boltInitialise() func(tx *bolt.Tx) error {
	return func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(resultBucket))
		return err
	}
}

// boltGetPkg gets the package name from the bolt datastore and stores in
// result, if result is not found, result will be nil
func getResult(pkg string) (*result, error) {
	var result *result
	err := db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket([]byte(resultBucket)).Get([]byte(pkg))
		if val == nil {
			// not found so just leave result
			return nil
		}

		var buf bytes.Buffer
		if _, err := buf.Write(val); err != nil {
			return fmt.Errorf("could not write result to buffer: %s", err)
		}

		dec := gob.NewDecoder(&buf)
		if err := dec.Decode(&result); err != nil {
			log.Printf("bytes: %s", buf.Bytes())
			return fmt.Errorf("could not decode result %s: %s", val, err)
		}
		return nil
	})
	return result, err
}

func boltPutResult(pkg string, result result) func(tx *bolt.Tx) error {
	return func(tx *bolt.Tx) error {
		var buf bytes.Buffer
		enc := gob.NewEncoder(&buf)
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("could not decode result: %s", err)
		}

		r := tx.Bucket([]byte(resultBucket)).Put([]byte(pkg), buf.Bytes())
		return r
	}
}
