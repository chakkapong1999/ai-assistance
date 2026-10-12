import type { NextConfig } from "next";

const config: NextConfig = {
  // Self-contained server output for the Docker image.
  output: "standalone",
  // "My work" became the inbox.
  redirects: async () => [{ source: "/my-work", destination: "/inbox", permanent: true }],
};

export default config;
