RUN: |
  set -e
  mkdir -p /var/cache/apk
  ln -s /var/cache/apk /etc/apk/cache || true
  
  # Install EFI bootloader and disk utilities
  apk add grub-efi dosfstools parted gptfdisk

  # Configure GRUB to route kernel output to our terminal (hvc0)
  mkdir -p /etc/default
  echo 'GRUB_TIMEOUT=5' > /etc/default/grub
  echo 'GRUB_TIMEOUT_STYLE="menu"' >> /etc/default/grub
  echo 'GRUB_CMDLINE_LINUX_DEFAULT="console=hvc0 oops=panic panic=10 loglevel=4 rootfstype=ext4 rw rootwait"' >> /etc/default/grub
  echo 'GRUB_TERMINAL="console"' >> /etc/default/grub

# CHANGED: Call the actual path to your OS-agnostic disk assembler
INCLUDE common/boot-efi.dockerfile
