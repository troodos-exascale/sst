




func createInstanceEFI(imageFile, name string) error {
	if err := ensureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir := instanceDir(name)
	if st, err := os.Stat(instDir); err == nil && st.IsDir() {
		if !force {
			return fmt.Errorf("instance %s exists (use --force)", name)
		}
		_ = os.RemoveAll(instDir)
	}
	if err := os.MkdirAll(instDir, 0755); err != nil {
		return err
	}

	// 1) Create disk.raw from source image
	dstDisk := filepath.Join(instDir, "disk.raw")

	if isQCOW2(imageFile) {
		logDebug("detected qcow2; running qemu-img convert")
		cmd := exec.Command("qemu-img", "convert", "-O", "raw", imageFile, dstDisk)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("qemu-img convert failed: %w", err)
		}
	} else {
		logDebug("assuming raw image; performing sparse copy")
		if err := sparseCopyRaw(imageFile, dstDisk); err != nil {
			return fmt.Errorf("sparse copy failed: %w", err)
		}
	}

	// 2) Create empty EFI vars
	efiVars := filepath.Join(instDir, "efi-vars")
	if err := ioutil.WriteFile(efiVars, []byte{}, 0644); err != nil {
		return fmt.Errorf("writing efi-vars: %w", err)
	}

	// 3) Create 30GB sparse data disk (default)
	dataImg := filepath.Join(instDir, "data.img")
	if _, err := os.Stat(dataImg); os.IsNotExist(err) {
		if err := createSparse(dataImg, 30*1024*1024*1024); err != nil {
			return fmt.Errorf("creating data.img: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat data.img: %w", err)
	}

	// 4) Add share /Users/$USER -> /Users/$USER
	u, _ := user.Current()
	hostShare := filepath.Join("/Users", u.Username)
	guestShare := filepath.Join("/Users", u.Username)

	// 5) Verbose kernel params (still stored even if EFI mode)
	params := "console=hvc0 root_device=dev/hda1 systemd.log_target=console"

	// 6) Defaults for CPU and memory
	defaultCPU := 4
	defaultMem := "4G"

	cfg := RuntimeConfig{
		Name:       name,
		Boot:       "efi",
		SystemDisk: dstDisk,
		EFIVars:    efiVars,
		Params:     params,
		CPUCount:   defaultCPU,
		MemorySize: defaultMem,
		DataDisks: []DataDisk{{ Path: filepath.Base(dataImg), Mode: "rw" }},
		Shares: []Share{
			{Host: hostShare, Guest: guestShare},
		},
	}
	mac, err := generateMAC()
	if err != nil {
		return err
	}
	cfg.Network.MAC = mac
	cfg.Network.Mode = "nat"

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(instDir, "runtime.yaml"), data, 0644)
}
