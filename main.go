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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/blake2b"
)

type Config struct {
	AccountID      string
	AccessKey      string
	AccessSecret   string
	Bucket         string
	Channel        string
	AppID          string
	Version        string
	Platform       string
	ExecutablePath string
}

type Manifest struct {
	Channel map[string]*Channel `json:"channel"`
}

type Channel struct {
	Version  string               `json:"version"`
	Build    time.Time            `json:"build"`
	Artifact map[string]*Artifact `json:"artifact"`
	Metadata map[string]any       `json:"metadata"`
}

type Artifact struct {
	Binary   string         `json:"binary"`
	Checksum string         `json:"checksum"`
	Patch    string         `json:"patch"`
	Metadata map[string]any `json:"metadata"`
}

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
}

func loadConfig() (*Config, error) {
	config := &Config{}
	envVars := []struct {
		ptr *string
		key string
	}{
		{&config.AccountID, "ACCOUNT_ID"},
		{&config.AccessKey, "ACCESS_KEY"},
		{&config.AccessSecret, "ACCESS_SECRET"},
		{&config.Bucket, "BUCKET"},
		{&config.Channel, "CHANNEL"},
		{&config.AppID, "APP_ID"},
		{&config.Version, "VERSION"},
		{&config.Platform, "PLATFORM"},
		{&config.ExecutablePath, "EXECUTABLE_PATH"},
	}

	for _, env := range envVars {
		value, exists := os.LookupEnv(env.key)
		if !exists {
			return nil, fmt.Errorf("%s is not set", env.key)
		}
		*env.ptr = value
	}

	return config, nil
}

func createR2Client(config *Config) (*minio.Core, error) {
	return minio.NewCore(
		fmt.Sprintf("%s.r2.cloudflarestorage.com", config.AccountID),
		&minio.Options{
			Secure: true,
			Creds:  credentials.NewStaticV4(config.AccessKey, config.AccessSecret, ""),
			Region: "auto",
		},
	)
}

func getManifest(ctx context.Context, r2 *minio.Core, bucket, appID string) (*Manifest, error) {
	reader, _, _, err := r2.GetObject(ctx, bucket, fmt.Sprintf("%s/manifest.json", appID), minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return &Manifest{Channel: make(map[string]*Channel)}, nil
		}
		return nil, fmt.Errorf("failed to get manifest: %w", err)
	}
	defer func(reader io.ReadCloser) {
		err := reader.Close()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to close reader")
		}
	}(reader)

	var manifest Manifest
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("failed to decode manifest: %w", err)
	}
	return &manifest, nil
}

func calculateChecksum(file *os.File) (string, error) {
	hasher, _ := blake2b.New256(nil)
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("failed to create checksum: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func uploadArtifact(ctx context.Context, r2 *minio.Core, config *Config, file *os.File, fileSize int64, checksum string) error {
	_, err := r2.Client.PutObject(ctx, config.Bucket, fmt.Sprintf("%s/artifact/%s", config.AppID, checksum), file, fileSize, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("failed to upload artifact: %w", err)
	}
	return nil
}

func uploadManifest(ctx context.Context, r2 *minio.Core, config *Config, manifest *Manifest) error {
	marshaledManifest, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	_, err = r2.Client.PutObject(ctx, config.Bucket, fmt.Sprintf("%s/manifest.json", config.AppID), bytes.NewReader(marshaledManifest), int64(len(marshaledManifest)), minio.PutObjectOptions{
		ContentType: "application/json",
	})
	if err != nil {
		return fmt.Errorf("failed to upload manifest: %w", err)
	}
	return nil
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	r2, err := createR2Client(config)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to R2")
	}

	ctx := context.Background()

	manifest, err := getManifest(ctx, r2, config.Bucket, config.AppID)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get manifest")
	}

	executable, err := os.Open(config.ExecutablePath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open executable")
	}
	defer func(executable *os.File) {
		err := executable.Close()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to close executable")
		}
	}(executable)

	executableStat, err := executable.Stat()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to stat executable")
	}

	checksum, err := calculateChecksum(executable)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to calculate checksum")
	}

	if _, err := executable.Seek(0, 0); err != nil {
		log.Fatal().Err(err).Msg("Failed to seek to beginning of executable")
	}

	if err := uploadArtifact(ctx, r2, config, executable, executableStat.Size(), checksum); err != nil {
		log.Fatal().Err(err).Msg("Failed to upload artifact")
	}

	log.Info().Msg("Artifact uploaded successfully")

	if manifest.Channel[config.Channel] == nil {
		manifest.Channel[config.Channel] = &Channel{
			Artifact: make(map[string]*Artifact),
		}
	}

	manifest.Channel[config.Channel].Version = config.Version
	manifest.Channel[config.Channel].Build = executableStat.ModTime()
	manifest.Channel[config.Channel].Artifact[config.Platform] = &Artifact{
		Binary:   fmt.Sprintf("%s/artifact/%s", config.AppID, checksum),
		Checksum: checksum,
	}

	if err := uploadManifest(ctx, r2, config, manifest); err != nil {
		log.Fatal().Err(err).Msg("Failed to upload manifest")
	}

	log.Info().Msg("Manifest uploaded successfully")
}
