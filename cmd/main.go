func handleBuild() {

	args := os.Args

	// Default tag
	tag := "latest"

	// Parse -t flag
	for i := 0; i < len(args); i++ {
		if args[i] == "-t" && i+1 < len(args) {
			tag = args[i+1]
		}
	}

	fmt.Println("Building image with tag:", tag)

	// Parse Docksmithfile
	instructions, err := parser.ParseDocksmithfile("Docksmithfile")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Println("Parsed Instructions:")

	for i, inst := range instructions {
		fmt.Printf("%d. %s %s\n", i+1, inst.Type, strings.Join(inst.Args, " "))
	}

	// ----------------------------
	// Create Tar (Layer)
	// ----------------------------
	err = storage.CreateTar(".", "layer.tar")
	if err != nil {
		fmt.Println("Tar error:", err)
		return
	}

	// ----------------------------
	// Save Layer (Content Addressing)
	// ----------------------------
	layerHash, err := storage.SaveLayer("layer.tar")
	if err != nil {
		fmt.Println("Save layer error:", err)
		return
	}

	fmt.Println("Layer hash:", layerHash)
}