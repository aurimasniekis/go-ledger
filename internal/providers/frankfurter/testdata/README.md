Fixtures are the response shapes of Frankfurter's v2 rates route (`/v2/rates`),
trimmed to the pairs the tests ask for. They were written by hand from one
observation of the live service and are never refreshed automatically: tests
use them through a local test server and never reach the live API.
