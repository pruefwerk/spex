{ pkgs ? import <nixpkgs> {}, unstable ? import <nixpkgs-unstable> {} }:

with pkgs;

mkShell {
  buildInputs = [
    unstable.kubernetes-helm
    unstable.kuttl
    unstable.kind
    unstable.kubectl
    unstable.k9s
    unstable.cloc
    unstable.golangci-lint
  ];
  shellHook = ''
    echo "Set DOCKER_HOST in your shell if your Docker daemon does not use the default socket."
  '';
}
