import type { Image } from '../lib/api/types';

// Placeholder garment drawings for the dev mock API. Real product photos come
// from the backend once catalog images exist.

type Shape = 'robe' | 'aodai' | 'gown' | 'coat' | 'cape' | 'suit' | 'hat' | 'mask';

const shapes: Record<Shape, (main: string, accent: string) => string> = {
  robe: (m, a) => `
    <path fill="${m}" d="M210 170h180l140 130-60 70-60-50 20 370H170l20-370-60 50-60-70z"/>
    <path fill="${a}" d="M262 170h76l-38 120z"/>
    <rect x="186" y="380" width="228" height="56" fill="${a}"/>
    <path stroke="${a}" stroke-width="10" fill="none" d="M300 290v400"/>`,
  aodai: (m, a) => `
    <path fill="${a}" d="M215 560h170l26 170h-90l-21-110-21 110h-90z"/>
    <path fill="${m}" d="M250 165h100l48 60 74 230-46 16-56-166 8 120 46 300h-108l-16-190-16 190H176l46-300 8-120-56 166-46-16 74-230z"/>
    <path fill="${a}" d="M270 165h60v26h-60z"/>`,
  gown: (m, a) => `
    <path fill="${m}" d="M246 170h108l22 150 124 380H100l124-380z"/>
    <path fill="${a}" d="M224 320h152l12 36H212z"/>
    <path fill="${a}" opacity=".35" d="M300 356l40 344h-80z"/>
    <path fill="${a}" d="M270 170h60l-30 50z"/>`,
  coat: (m, a) => `
    <path fill="${m}" d="M220 165h160l110 110-30 330-70-20-6-250-14 420H230l-14-420-6 250-70 20-30-330z"/>
    <path fill="${a}" d="M270 165h60l-30 110z"/>
    <g fill="${a}"><circle cx="300" cy="330" r="11"/><circle cx="300" cy="400" r="11"/><circle cx="300" cy="470" r="11"/><circle cx="300" cy="540" r="11"/></g>`,
  cape: (m, a) => `
    <path fill="${a}" d="M200 190h200l110 510H90z"/>
    <path fill="${m}" d="M245 170h110l40 90-24 210 14 250h-72l-13-210-13 210h-72l14-250-24-210z"/>
    <path fill="${a}" d="M262 300l38-40 38 40-38 54z"/>`,
  suit: (m, a) => `
    <path fill="${m}" d="M225 165h150l100 90-26 240-56-10-10-170 8 440h-84l-7-260-7 260h-84l8-440-10 170-56 10-26-240z"/>
    <rect x="212" y="400" width="176" height="30" fill="${a}"/>
    <path fill="${a}" d="M268 165h64l-32 60z"/>`,
  hat: (m, a) => `
    <path fill="${m}" d="M300 150l110 300H190z"/>
    <path fill="${m}" d="M110 450h380l40 50H70z"/>
    <rect x="186" y="410" width="228" height="40" fill="${a}"/>`,
  mask: (m, a) => `
    <path fill="${m}" d="M110 300c60-40 130-40 190 0 60-40 130-40 190 0 10 110-40 210-130 210-30 0-40-40-60-40s-30 40-60 40c-90 0-140-100-130-210z"/>
    <ellipse cx="205" cy="375" rx="42" ry="26" fill="${a}"/><ellipse cx="395" cy="375" rx="42" ry="26" fill="${a}"/>`,
};

function svg(shape: Shape, main: string, accent: string, bg: string, zoom: boolean): string {
  const view = zoom ? '120 110 360 480' : '0 0 600 800';
  const hexes = [[60, 90], [500, 150], [90, 640], [470, 620], [300, 60]]
    .map(([x, y]) => `<path fill="#fff" opacity=".1" transform="translate(${x} ${y})" d="M40 0 76 20v40L40 80 4 60V20z"/>`).join('');
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${view}"><rect x="-200" y="-200" width="1000" height="1200" fill="${bg}"/>${hexes}${shapes[shape](main, accent)}</svg>`;
}

function uri(content: string): string {
  return `data:image/svg+xml,${encodeURIComponent(content)}`;
}

export function art(name: string, shape: Shape, main: string, accent: string, bg: string): Image[] {
  return [
    { url: uri(svg(shape, main, accent, bg, false)), alt: `${name}, ảnh toàn thân` },
    { url: uri(svg(shape, main, accent, bg, true)), alt: `${name}, ảnh cận cảnh` },
  ];
}
