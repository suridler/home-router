'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {errorHandler} = require('../../middleware/error-handler');
const {HelperClientError} = require('../../services/helper-client');
test('shared error middleware preserves a typed helper failure',()=>{
 let status,body;
 errorHandler(new HelperClientError('Setup failed',{code:'setup_failed',statusCode:503}),{method:'POST',path:'/api/setup/apply'},{headersSent:false,status(value){status=value;return this;},json(value){body=value;}},()=>assert.fail('unexpected next'));
 assert.equal(status,503);assert.equal(body.code,'setup_failed');assert.equal(body.message,'Setup failed');
});
