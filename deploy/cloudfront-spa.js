// Associate ONLY with the default S3 cache behavior (viewer-request).
// Never globally replace API 403/404 responses with index.html.
function handler(event) {
  var request = event.request;
  var uri = request.uri;
  if (uri === '/api' || uri.indexOf('/api/') === 0) return request;
  if (uri.endsWith('/') || uri.split('/').pop().indexOf('.') === -1) {
    request.uri = '/index.html';
  }
  return request;
}
