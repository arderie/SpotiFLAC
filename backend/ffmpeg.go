package backend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

func ValidateExecutable(path string) error {
	cleanedPath := filepath.Clean(path)
	if cleanedPath == "" {
		return fmt.Errorf("empty path")
	}

	if !filepath.IsAbs(cleanedPath) {
		return fmt.Errorf("path must be absolute: %s", path)
	}

	info, err := os.Stat(cleanedPath)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	if info.IsDir() {
		return fmt.Errorf("path is a directory: %s", path)
	}

	if runtime.GOOS != "windows" {
		if info.Mode()&0111 == 0 {
			return fmt.Errorf("file is not executable: %s", path)
		}
	}

	base := filepath.Base(cleanedPath)
	validNames := map[string]bool{
		"ffmpeg":      true,
		"ffmpeg.exe":  true,
		"ffprobe":     true,
		"ffprobe.exe": true,
	}
	if !validNames[base] {
		return fmt.Errorf("invalid executable name: %s", base)
	}

	return nil
}

func GetFFmpegDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".spotiflac"), nil
}

func GetFFmpegPath() (string, error) {
	ffmpegDir, err := GetFFmpegDir()
	if err != nil {
		return "", err
	}

	ffmpegName := "ffmpeg"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
	}

	localPath := filepath.Join(ffmpegDir, ffmpegName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	path, err := exec.LookPath(ffmpegName)
	if err == nil {
		return path, nil
	}

	return localPath, nil
}

func GetFFprobePath() (string, error) {
	ffmpegDir, err := GetFFmpegDir()
	if err != nil {
		return "", err
	}

	ffprobeName := "ffprobe"
	if runtime.GOOS == "windows" {
		ffprobeName = "ffprobe.exe"
	}

	localPath := filepath.Join(ffmpegDir, ffprobeName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	path, err := exec.LookPath(ffprobeName)
	if err == nil {
		return path, nil
	}

	return localPath, fmt.Errorf("ffprobe not found in app directory or system path")
}

func IsFFprobeInstalled() (bool, error) {
	ffprobePath, err := GetFFprobePath()
	if err != nil {
		return false, nil
	}

	if err := ValidateExecutable(ffprobePath); err != nil {
		return false, nil
	}

	cmd := exec.Command(ffprobePath, "-version")
	setHideWindow(cmd)
	err = cmd.Run()
	return err == nil, nil
}

func IsFFmpegInstalled() (bool, error) {
	ffmpegPath, err := GetFFmpegPath()
	if err != nil {
		return false, err
	}

	if err := ValidateExecutable(ffmpegPath); err != nil {
		return false, nil
	}

	cmd := exec.Command(ffmpegPath, "-version")
	setHideWindow(cmd)
	err = cmd.Run()
	return err == nil, nil
}

// DownloadFFmpeg is intentionally disabled. FFmpeg must be installed manually.
func DownloadFFmpeg(progressCallback func(int)) error {
	return fmt.Errorf("automatic FFmpeg download is disabled; please install FFmpeg manually")
}

type ConvertAudioRequest struct {
	InputFiles   []string `json:"input_files"`
	OutputFormat string   `json:"output_format"`
	Bitrate      string   `json:"bitrate"`
	Codec        string   `json:"codec"`
}

type ConvertAudioResult struct {
	InputFile  string `json:"input_file"`
	OutputFile string `json:"output_file"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

func ConvertAudio(req ConvertAudioRequest) ([]ConvertAudioResult, error) {
	ffmpegPath, err := GetFFmpegPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get ffmpeg path: %w", err)
	}

	if err := ValidateExecutable(ffmpegPath); err != nil {
		return nil, fmt.Errorf("invalid ffmpeg executable: %w", err)
	}

	installed, err := IsFFmpegInstalled()
	if err != nil || !installed {
		return nil, fmt.Errorf("ffmpeg is not installed")
	}

	results := make([]ConvertAudioResult, len(req.InputFiles))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, inputFile := range req.InputFiles {
		wg.Add(1)
		go func(idx int, inputFile string) {
			defer wg.Done()

			result := ConvertAudioResult{
				InputFile: inputFile,
			}

			inputExt := strings.ToLower(filepath.Ext(inputFile))
			baseName := strings.TrimSuffix(filepath.Base(inputFile), inputExt)
			inputDir := filepath.Dir(inputFile)

			outputFormatUpper := strings.ToUpper(req.OutputFormat)
			outputDir := filepath.Join(inputDir, outputFormatUpper)

			if err := os.MkdirAll(outputDir, 0755); err != nil {
				result.Error = fmt.Sprintf("failed to create output directory: %v", err)
				result.Success = false
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				return
			}

			outputExt := "." + strings.ToLower(req.OutputFormat)
			outputFile := filepath.Join(outputDir, baseName+outputExt)

			if inputExt == outputExt {
				result.Error = "Input and output formats are the same"
				result.Success = false
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				return
			}

			result.OutputFile = outputFile

			var coverArtPath string
			var lyrics string
			var inputMetadata Metadata

			inputMetadata, err = ExtractFullMetadataFromFile(inputFile)
			if err != nil {
				fmt.Printf("[FFmpeg] Warning: Failed to extract metadata from %s: %v\n", inputFile, err)
			}

			coverArtPath, _ = ExtractCoverArt(inputFile)
			lyrics, err = ExtractLyrics(inputFile)
			if err != nil {
				fmt.Printf("[FFmpeg] Warning: Failed to extract lyrics from %s: %v\n", inputFile, err)
			} else if lyrics != "" {
				fmt.Printf("[FFmpeg] Lyrics extracted from %s: %d characters\n", inputFile, len(lyrics))
			} else {
				fmt.Printf("[FFmpeg] No lyrics found in %s\n", inputFile)
			}

			inputMetadata.Lyrics = lyrics

			args := []string{
				"-i", inputFile,
				"-y",
			}

			switch req.OutputFormat {
			case "mp3":
				args = append(args,
					"-codec:a", "libmp3lame",
					"-b:a", req.Bitrate,
					"-map", "0:a",
					"-id3v2_version", "3",
				)
			case "m4a":
				codec := req.Codec
				if codec == "" {
					codec = "aac"
				}

				if codec == "alac" {
					args = append(args,
						"-codec:a", "alac",
						"-map", "0:a",
					)
				} else {
					args = append(args,
						"-codec:a", "aac",
						"-b:a", req.Bitrate,
						"-map", "0:a",
					)
				}
			}

			args = append(args, outputFile)

			fmt.Printf("[FFmpeg] Converting: %s -> %s\n", inputFile, outputFile)

			cmd := exec.Command(ffmpegPath, args...)
			setHideWindow(cmd)
			output, err := cmd.CombinedOutput()
			if err != nil {
				result.Error = fmt.Sprintf("conversion failed: %s - %s", err.Error(), string(output))
				result.Success = false
				mu.Lock()
				results[idx] = result
				mu.Unlock()

				if coverArtPath != "" {
					os.Remove(coverArtPath)
				}
				return
			}

			if err := EmbedMetadataToConvertedFile(outputFile, inputMetadata, coverArtPath); err != nil {
				fmt.Printf("[FFmpeg] Warning: Failed to embed metadata: %v\n", err)
			} else {
				fmt.Printf("[FFmpeg] Metadata embedded successfully\n")
			}

			if lyrics != "" {
				if err := EmbedLyricsOnlyUniversal(outputFile, lyrics); err != nil {
					fmt.Printf("[FFmpeg] Warning: Failed to embed lyrics: %v\n", err)
				} else {
					fmt.Printf("[FFmpeg] Lyrics embedded successfully\n")
				}
			}

			if coverArtPath != "" {
				os.Remove(coverArtPath)
			}

			result.Success = true
			fmt.Printf("[FFmpeg] Successfully converted: %s\n", outputFile)

			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, inputFile)
	}

	wg.Wait()
	return results, nil
}

type AudioFileInfo struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Format   string `json:"format"`
	Size     int64  `json:"size"`
}

func GetAudioFileInfo(filePath string) (*AudioFileInfo, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
	return &AudioFileInfo{
		Path:     filePath,
		Filename: filepath.Base(filePath),
		Format:   ext,
		Size:     info.Size(),
	}, nil
}
