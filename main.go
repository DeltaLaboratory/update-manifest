package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/crypto/blake2b"
)

func main() {
	AccountID, exists := os.LookupEnv("ACCOUNT_ID")
	if !exists {
		fmt.Println("E: ACCOUNT_ID is not set")
		os.Exit(1)
	}

	AccessKey, exists := os.LookupEnv("ACCESS_KEY")
	if !exists {
		fmt.Println("E: ACCESS_KEY is not set")
		os.Exit(1)
	}

	AccessSecret, exists := os.LookupEnv("ACCESS_SECRET")
	if !exists {
		fmt.Println("E: ACCESS_SECRET is not set")
		os.Exit(1)
	}

	Bucket, exists := os.LookupEnv("BUCKET")
	if !exists {
		fmt.Println("E: BUCKET is not set")
		os.Exit(1)
	}

	Channel, exists := os.LookupEnv("CHANNEL")
	if !exists {
		fmt.Println("E: CHANNEL is not set")
		os.Exit(1)
	}

	AppID, exists := os.LookupEnv("APP_ID")
	if !exists {
		fmt.Println("E: APP_ID is not set")
		os.Exit(1)
	}

	Version, exists := os.LookupEnv("VERSION")
	if !exists {
		fmt.Println("E: VERSION is not set")
		os.Exit(1)
	}

	Platform, exists := os.LookupEnv("PLATFORM")
	if !exists {
		fmt.Println("E: PLATFORM is not set")
		os.Exit(1)
	}

	ExecutablePath, exists := os.LookupEnv("EXECUTABLE_PATH")
	if !exists {
		fmt.Println("E: EXECUTABLE_PATH is not set")
		os.Exit(1)
	}

	executable, err := os.Open(ExecutablePath)
	if err != nil {
		fmt.Printf("E: Failed to open executable: %v\n", err)
		os.Exit(1)
	}

	executableStat, err := executable.Stat()
	if err != nil {
		fmt.Printf("E: Failed to stat executable: %v\n", err)
		os.Exit(1)
	}

	r2, err := minio.NewCore(fmt.Sprintf("%s.r2.cloudflarestorage.com", AccountID), &minio.Options{
		Secure: true,
		Creds:  credentials.NewStaticV4(AccessKey, AccessSecret, ""),
		Region: "auto",
	})

	if err != nil {
		fmt.Printf("E: Failed to connect to r2: %v\n", err)
		os.Exit(1)
	}

	var manifest Manifest

	// lookup if manifest exists
	reader, _, _, err := r2.GetObject(context.Background(), Bucket, fmt.Sprintf("%s/manifest.json", AppID), minio.GetObjectOptions{})
	if err == nil {
		if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
			fmt.Printf("E: Failed to decode manifest: %v\n", err)
			os.Exit(1)
		}
	}

	if manifest.Channel == nil {
		manifest.Channel = make(map[string]*struct {
			Version string    `json:"version"`
			Build   time.Time `json:"build"`

			Artifact map[string]*struct {
				Binary   string `json:"binary"`
				Checksum string `json:"checksum"`
				Patch    string `json:"patch"`
			} `json:"artifact"`
		})
	}

	if _, ok := manifest.Channel[Channel]; !ok {
		manifest.Channel[Channel] = &struct {
			Version string    `json:"version"`
			Build   time.Time `json:"build"`

			Artifact map[string]*struct {
				Binary   string `json:"binary"`
				Checksum string `json:"checksum"`
				Patch    string `json:"patch"`
			} `json:"artifact"`
		}{
			Artifact: make(map[string]*struct {
				Binary   string `json:"binary"`
				Checksum string `json:"checksum"`
				Patch    string `json:"patch"`
			}),
		}
	}

	if _, ok := manifest.Channel[Channel].Artifact[Platform]; !ok {
		manifest.Channel[Channel].Artifact[Platform] = &struct {
			Binary   string `json:"binary"`
			Checksum string `json:"checksum"`
			Patch    string `json:"patch"`
		}{}
	}

	manifest.Channel[Channel].Version = Version

	// create blake2b checksum
	hasher, _ := blake2b.New256(nil)
	if _, err := io.Copy(hasher, executable); err != nil {
		fmt.Printf("E: Failed to create checksum: %v\n", err)
		os.Exit(1)
	}

	manifest.Channel[Channel].Artifact[Platform].Checksum = hex.EncodeToString(hasher.Sum(nil))

	_, err = executable.Seek(0, 0)
	if err != nil {
		fmt.Printf("E: Failed to seek to beginning of executable: %v\n", err)
		os.Exit(1)
	}

	_, err = r2.Client.PutObject(context.Background(), Bucket, fmt.Sprintf("%s/artifect/%s", AppID, manifest.Channel[Channel].Artifact[Platform].Checksum), executable, executableStat.Size(), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		fmt.Printf("E: Failed to upload artifact: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("I: Artifact uploaded successfully")

	manifest.Channel[Channel].Artifact[Platform].Binary = fmt.Sprintf("%s/artifect/%s", AppID, manifest.Channel[Channel].Artifact[Platform].Checksum)
	manifest.Channel[Channel].Build = executableStat.ModTime()

	marshaledManifest, err := json.Marshal(manifest)
	if err != nil {
		fmt.Printf("E: Failed to marshal manifest: %v\n", err)
		os.Exit(1)
	}

	_, err = r2.Client.PutObject(context.Background(), Bucket, fmt.Sprintf("%s/manifest.json", AppID), bytes.NewReader(marshaledManifest), int64(len(marshaledManifest)), minio.PutObjectOptions{
		ContentType: "application/json",
	})

	if err != nil {
		fmt.Printf("E: Failed to upload manifest: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("I: Manifest uploaded successfully")
}

type Manifest struct {
	// Channel can be "stable" or "beta"
	Channel map[string]*struct {
		Version string    `json:"version"`
		Build   time.Time `json:"build"`

		Artifact map[string]*struct {
			Binary   string `json:"binary"`
			Checksum string `json:"checksum"`
			Patch    string `json:"patch"`
		} `json:"artifact"`
	} `json:"channel"`
}
