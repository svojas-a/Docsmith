package parser

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Instruction struct {
	Type string
	Args []string
}

func ParseDocksmithfile(path string) ([]Instruction, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var instructions []Instruction
	validInstructions := map[string]bool{
		"FROM":    true,
		"WORKDIR": true,
		"COPY":    true,
		"RUN":     true,
		"CMD":     true,
		"ENV":     true, // was missing in original
	}

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		instType := strings.ToUpper(parts[0])
		if !validInstructions[instType] {
			return nil, fmt.Errorf("line %d: invalid instruction: %s", lineNum, instType)
		}
		instructions = append(instructions, Instruction{
			Type: instType,
			Args: parts[1:],
		})
	}
	return instructions, nil
}