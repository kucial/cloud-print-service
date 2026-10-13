import { Printer } from './types';

// Printers from servers before receipt and office printers carry no capabilities: they
// were all label printers, with every tool.
export function hasCapability(p: Printer, capability: string): boolean {
  return p.capabilities ? p.capabilities.includes(capability) : true;
}

export function printerKind(p: Printer): 'label' | 'receipt' | 'document' {
  return p.kind || 'label';
}

// sizeTag is what the size column shows: the roll for label printers, the roll width for
// receipt printers, the sheet for office printers.
export function sizeTag(p: Printer): string | undefined {
  switch (printerKind(p)) {
    case 'receipt':
      return `${p.media_width_mm || 80}mm`;
    case 'document':
      return p.paper_size || 'A4';
    default:
      return p.paper_size || undefined;
  }
}
