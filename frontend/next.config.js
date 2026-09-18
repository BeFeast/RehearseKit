/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'standalone',
  reactStrictMode: true,
  async headers() {
    // Cross-origin isolation for the streaming playback lab only: the lab
    // needs SharedArrayBuffer for its AudioWorklet ring buffers. Scoped to
    // /lab/stream so Google sign-in and the rest of the app are unaffected.
    return [
      {
        source: '/lab/stream/:path*',
        headers: [
          { key: 'Cross-Origin-Opener-Policy', value: 'same-origin' },
          { key: 'Cross-Origin-Embedder-Policy', value: 'credentialless' },
        ],
      },
    ];
  },
  // Don't set env here - let environment variables be used directly
  images: {
    remotePatterns: [
      {
        protocol: 'https',
        hostname: 'storage.googleapis.com',
        pathname: '/rehearsekit-*/**',
      },
    ],
  },
}

module.exports = nextConfig

