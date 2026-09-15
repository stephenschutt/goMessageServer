#!/usr/bin/env node
// The webapp's CLI: it produces the version of the app the Go server serves.
//
//   node cli.js build     minified production build into dist/
//   node cli.js dev       unminified build that rebuilds as you edit
//   node cli.js clean     delete dist/
//
// npm run build / dev / clean are thin wrappers around these.
'use strict';

const fs = require('fs');
const path = require('path');
const webpack = require('webpack');

const dist = path.resolve(__dirname, 'dist');
const makeConfig = require('./webpack.config.js');

const usage = `messages-webapp — build the React client served at /webapp

Usage:
  node cli.js build [--dev]   build into dist/ (minified unless --dev)
  node cli.js dev             build unminified and watch for changes
  node cli.js clean           remove dist/
  node cli.js help            this message

The Go server embeds dist/ at compile time, so after a build rebuild the
server too: go build -o messageServer .`;

function main(argv) {
  const command = argv[0] || 'help';
  switch (command) {
    case 'build':
      return build({ mode: argv.includes('--dev') ? 'development' : 'production' });
    case 'dev':
      return build({ mode: 'development', watch: true });
    case 'clean':
      return clean();
    case 'help':
    case '--help':
    case '-h':
      console.log(usage);
      return 0;
    default:
      console.error(`unknown command "${command}"\n\n${usage}`);
      return 2;
  }
}

function clean() {
  fs.rmSync(dist, { recursive: true, force: true });
  console.log(`removed ${path.relative(process.cwd(), dist) || dist}`);
  return 0;
}

function build({ mode, watch = false }) {
  const config = makeConfig(undefined, { mode });
  const compiler = webpack(config);

  // A watch build never "finishes", so it reports each pass and keeps going;
  // a one-shot build exits with a status a script can act on.
  const done = (err, stats) => {
    if (report(err, stats) && !watch) {
      process.exitCode = 1;
      return;
    }
    if (!watch) summarize(mode);
  };

  if (watch) {
    console.log(`watching src/ — ${mode} build, Ctrl-C to stop`);
    compiler.watch({ aggregateTimeout: 200 }, done);
  } else {
    compiler.run((err, stats) => {
      done(err, stats);
      compiler.close(() => {});
    });
  }
  return 0;
}

/// report prints whatever went wrong and says whether the build failed.
function report(err, stats) {
  if (err) {
    console.error(err.stack || String(err));
    return true;
  }
  const output = stats.toString({ colors: process.stdout.isTTY, preset: 'errors-warnings' });
  if (output.trim()) console.error(output);
  return stats.hasErrors();
}

/// summarize lists what landed in dist/, because the file names are hashed and
/// the next step — rebuilding the Go binary — depends on them.
function summarize(mode) {
  const files = fs.existsSync(dist) ? fs.readdirSync(dist).sort() : [];
  console.log(`${mode} build -> ${path.relative(process.cwd(), dist) || dist}`);
  for (const name of files) {
    const { size } = fs.statSync(path.join(dist, name));
    console.log(`  ${name.padEnd(28)} ${(size / 1024).toFixed(1)} KiB`);
  }
  console.log('\nthe server embeds dist/, so rebuild it to serve this:\n  go build -o messageServer .');
}

process.exitCode = main(process.argv.slice(2)) || process.exitCode || 0;
