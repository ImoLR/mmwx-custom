// Text on the compact node/user/package pages must not drop below the quick-filter size.
export const fontFloor = Number(process.env.FONT_FLOOR || 13);
export const enforceFontFloor = process.env.FONT_FLOOR_ASSERT !== "0";

export async function smallText(page) {
  return page.evaluate((floor) => {
    const scopes = [...document.querySelectorAll(".node-redesign, .cx-manager, .cx-sheet")];
    const found = new Map();
    for (const scope of scopes) for (const element of scope.querySelectorAll("*")) {
      if (element.closest("svg")) continue;
      const text = [...element.childNodes].filter((node) => node.nodeType === Node.TEXT_NODE).map((node) => node.textContent.trim()).join(" ").trim();
      if (!text || !element.getClientRects().length) continue;
      const style = getComputedStyle(element);
      if (style.visibility === "hidden" || Number(style.opacity) === 0) continue;
      const size = parseFloat(style.fontSize);
      if (size >= floor) continue;
      const key = `${element.tagName.toLowerCase()}.${[...element.classList].join(".")} ${size}px`;
      if (!found.has(key)) found.set(key, text.slice(0, 40));
    }
    return [...found].map(([element, text]) => ({ element, text }));
  }, fontFloor);
}
