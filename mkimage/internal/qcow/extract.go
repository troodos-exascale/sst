package qcow

import (
    "fmt"
    "io"
    "os"
    "path/filepath"
)

func ExtractKernelInitrd(mnt string, dest string) (string, string, error) {
    // Find kernel
    kernel := ""
    initrd := ""

    filepath.Walk(mnt+"/boot", func(path string, info os.FileInfo, _ error) error {
        if info == nil || info.IsDir() { return nil }

        switch {
        case info.Name() == "vmlinuz" || filepath.Ext(path) == "":
            if kernel == "" && (info.Name() == "vmlinuz" || 
                (len(info.Name()) > 7 && info.Name()[:7] == "vmlinuz")) {
                kernel = path
            }
        case info.Name() == "initrd.img" || info.Name() == "initrd":
            if initrd == "" {
                initrd = path
            }
        }
        return nil
    })

    if kernel == "" || initrd == "" {
        return "", "", fmt.Errorf("could not find kernel or initrd under %s/boot", mnt)
    }

    // Copy kernel
    kdst := filepath.Join(dest, "kernel")
    if err := copyFile(kernel, kdst); err != nil { return "", "", err }

    // Copy initrd
    idst := filepath.Join(dest, "initrd")
    if err := copyFile(initrd, idst); err != nil { return "", "", err }

    return kdst, idst, nil
}

func copyFile(src, dst string) error {
    in, err := os.Open(src)
    if err != nil { return err }
    defer in.Close()

    out, err := os.Create(dst)
    if err != nil { return err }
    defer out.Close()

    _, err = io.Copy(out, in)
    return err
}
