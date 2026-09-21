package util

import (
	"bufio"
	"fmt"
	"os"
)

func GetLines(filePath string, m, n int) ([]string, error) {
	if m < 1 || n < m {
		return nil, fmt.Errorf("invalid range: m (%d) must be >= 1 and n (%d) must be >= m", m, n)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	currentLine := 0

	for scanner.Scan() {
		currentLine++
		if currentLine >= m && currentLine <= n {
			lines = append(lines, scanner.Text())
		}
		if currentLine > n {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %v", err)
	}

	return lines, nil
}
