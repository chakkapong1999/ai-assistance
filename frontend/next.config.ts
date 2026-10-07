import type { NextConfig } from "next";

const config: NextConfig = {
  // Self-contained server output for the Docker image.
  output: "standalone",
};

export default config;
