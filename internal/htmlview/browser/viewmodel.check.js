// Holds the view model Go writes to the shape page.js reads: tsc fails here
// when Go drops or renames a key the script's types require. The golden comes
// from the map fixture, which produces every kind of node and cross-link.
import model from "../testdata/map.viewmodel.json" with { type: "json" };

/** @type {import("../page.js").ViewModel} */
export const checked = model;
