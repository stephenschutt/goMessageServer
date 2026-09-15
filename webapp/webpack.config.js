// Webpack build for the React client the Go server serves at /webapp.
//
// The output goes to dist/, which messageServer embeds with go:embed — so a
// change here only reaches the browser after both `node cli.js build` and a
// rebuild of the Go binary. The CLI says so when it finishes.
'use strict';

const path = require('path');
const HtmlWebpackPlugin = require('html-webpack-plugin');

module.exports = (env, argv = {}) => {
  const production = argv.mode !== 'development';

  return {
    mode: production ? 'production' : 'development',
    entry: './src/index.jsx',
    output: {
      path: path.resolve(__dirname, 'dist'),
      // Content-hashed so a new build can never be served from a stale cache,
      // which matters because the page and its bundle ship together.
      filename: production ? 'webapp.[contenthash:8].js' : 'webapp.js',
      // Absolute, because the app is mounted under /webapp/ rather than at the
      // root: a relative path would break the moment a URL gained a segment.
      publicPath: '/webapp/',
      clean: true,
    },
    resolve: { extensions: ['.js', '.jsx'] },
    module: {
      rules: [
        {
          test: /\.jsx?$/,
          exclude: /node_modules/,
          use: {
            loader: 'babel-loader',
            options: {
              presets: [
                ['@babel/preset-env', { targets: '> 0.5%, last 2 versions, not dead' }],
                ['@babel/preset-react', { runtime: 'automatic' }],
              ],
            },
          },
        },
        { test: /\.css$/, use: ['style-loader', 'css-loader'] },
      ],
    },
    plugins: [
      new HtmlWebpackPlugin({
        template: './src/index.html',
        minify: production && {
          collapseWhitespace: true,
          removeComments: true,
          minifyCSS: true,
        },
      }),
    ],
    // A production build is minified by webpack's own defaults; the source map
    // is kept out of it so the private-key handling is not shipped in readable
    // form next to the bundle.
    devtool: production ? false : 'eval-source-map',
    performance: { hints: false },
    stats: 'errors-warnings',
  };
};
