#DataDisk: {
    path: string
    mode: *"rw" | "ro"
}

#Runtime: {
    name:        string
    boot:        "efi" | "kernel"

    system_disk: string
    cidata_disk?: string
    data_disks?: [...#DataDisk]

    efi_vars:    string
    params:      string
    cpu_count:   int & >0
    memory_size: string

    shares?: [...{
        host:  string
        guest: string
    }]

    network: {
        mac:  string
        mode: string
    }
}

runtime: #Runtime
