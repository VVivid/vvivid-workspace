import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'VocabBank',
  description: 'Your private English vocabulary bank.',
};
export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body>{children}</body>
    </html>
  );
}
