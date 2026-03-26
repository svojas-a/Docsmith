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
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)

		instType := strings.ToUpper(parts[0])

		if !validInstructions[instType] {
			return nil, fmt.Errorf("invalid instruction: %s", instType)
		}

		instruction := Instruction{
			Type: instType,
			Args: parts[1:],
		}

		instructions = append(instructions, instruction)
	}

	return instructions, nil
}
