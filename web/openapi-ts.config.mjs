// hey-api chooses import extensions from the resolved tsconfig — none by
// default, `.js` under moduleResolution node16/nodenext — so the same spec
// produces different bytes on different machines. The generated output is
// committed and gated on byte-identity, so the extension is pinned here and the
// output becomes a function of this file.
//
// Input and output live here rather than on the command line because passing
// -i/-o replaces the whole output object, which drops the pin with it.
export default {
  input: '../api/openapi.yaml',
  output: {
    path: 'src/api/gen',
    module: { extension: '.js' },
  },
};
