import { useTheme } from '../lib/use-theme';
import markLight from '../assets/logo/mark.svg';
import markDark from '../assets/logo/mark-dark.svg';

/** The 24px mark; swaps artwork under the dark theme (README gap #dark-logo). */
export function LogoMark({ size = 24 }: { size?: number }) {
  const theme = useTheme();
  return <img src={theme === 'dark' ? markDark : markLight} alt="" width={size} height={size} />;
}
