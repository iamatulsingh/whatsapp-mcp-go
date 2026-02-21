package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConvertToOpusOggTemp – creates a temporary .ogg file in Opus format
// Uses reasonable defaults for WhatsApp voice messages
func ConvertToOpusOggTemp(inputFile string) (string, error) {
	// WhatsApp voice messages usually use:
	//   bitrate  → 24k–32k is very common and good quality/size balance
	//   sample rate → 48000 Hz (Opus native)
	const defaultBitrate = "32k"
	const defaultSampleRate = 48000

	return ConvertToOpusOggTempWithParams(inputFile, defaultBitrate, defaultSampleRate)
}

// ConvertToOpusOgg converts an audio file to Opus format in an Ogg container.
func ConvertToOpusOgg(inputFile string, outputFile string, bitrate string, sampleRate int) (string, error) {
	// Check if input file exists
	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		return "", fmt.Errorf("input file not found: %s", inputFile)
	}

	// If no output file is specified, replace extension with .ogg
	if outputFile == "" {
		ext := filepath.Ext(inputFile)
		outputFile = strings.TrimSuffix(inputFile, ext) + ".ogg"
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(outputFile)
	if outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	// Build the ffmpeg command
	cmd := exec.Command("ffmpeg",
		"-i", inputFile,
		"-c:a", "libopus",
		"-b:a", bitrate,
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-application", "voip",
		"-vbr", "on",
		"-compression_level", "10",
		"-frame_duration", "60",
		"-y",
		outputFile,
	)

	// Run command and capture output for error reporting
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to convert audio. ffmpeg error: %s (%w)", string(output), err)
	}

	return outputFile, nil
}

// ConvertToOpusOggTempWithParams converts audio to a temporary .ogg file.
func ConvertToOpusOggTempWithParams(inputFile string, bitrate string, sampleRate int) (string, error) {
	// Create a temporary file
	tempFile, err := os.CreateTemp("", "audio-*.ogg")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	err = tempFile.Close()
	if err != nil {
		return "", err
	} // Close it so ffmpeg can write to the path

	// Convert the audio
	result, err := ConvertToOpusOgg(inputFile, tempPath, bitrate, sampleRate)
	if err != nil {
		// Clean up on failure
		err := os.Remove(tempPath)
		if err != nil {
			return "", err
		}
		return "", err
	}

	return result, nil
}
