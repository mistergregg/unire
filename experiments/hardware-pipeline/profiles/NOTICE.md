# Third-party notice: Cura definition files

The following files are copied from Ultimaker Cura's `resources/definitions/` directory. `fdmprinter.def.json` defines Cura's base FDM machine settings; `fdmextruder.def.json` defines the FDM extruder train settings used by CuraEngine.

| File | Exact source URL | SHA-256 |
| --- | --- | --- |
| `fdmprinter.def.json` | <https://raw.githubusercontent.com/Ultimaker/Cura/4.13.0/resources/definitions/fdmprinter.def.json> | `6634679e3a9571f877e52e57a688d883dc4dc9fe6855a04c3b7be19b60f3a0b7` |
| `fdmextruder.def.json` | <https://raw.githubusercontent.com/Ultimaker/Cura/4.13.0/resources/definitions/fdmextruder.def.json> | `c03847252f9dea37277a3151c0eaeec32ded5e4cd91eed62b58e420ad8cb7fef` |

Both URLs refer to Cura release tag [`4.13.0`](https://github.com/Ultimaker/Cura/tree/4.13.0), which resolves to commit `c328f3619dae3ad37fb1decccf1013afa18b410c`. The corresponding source files are [`fdmprinter.def.json`](https://github.com/Ultimaker/Cura/blob/c328f3619dae3ad37fb1decccf1013afa18b410c/resources/definitions/fdmprinter.def.json) and [`fdmextruder.def.json`](https://github.com/Ultimaker/Cura/blob/c328f3619dae3ad37fb1decccf1013afa18b410c/resources/definitions/fdmextruder.def.json).

## License

The Cura repository's [`LICENSE` at that commit](https://github.com/Ultimaker/Cura/blob/c328f3619dae3ad37fb1decccf1013afa18b410c/LICENSE) identifies these repository files as GNU Lesser General Public License, version 3 (LGPL-3.0). Its header reads:

> GNU LESSER GENERAL PUBLIC LICENSE  
> Version 3, 29 June 2007

The JSON files themselves have no separate license or copyright header. The LGPL-3.0 text is also available at [gnu.org](https://www.gnu.org/licenses/lgpl-3.0.html). These copied files remain under LGPL-3.0; the repository's AGPL-3.0 license does not relicense them.

## Retrieval and integrity

Retrieved from the Cura `4.13.0` tag on 2026-11-21. Each file was downloaded again from its source URL and compared byte-for-byte with the repository copy (`cmp`); the SHA-256 values above match both downloaded and committed files. The files are unmodified.

`petg_0.2mm.json` is an original profile written for this prototype; it is not copied from Cura.
