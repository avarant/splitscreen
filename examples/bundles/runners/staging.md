# This runner

You are driving the staging environment at `/var/www/app`.

- Restart services with `sudo supervisorctl restart app:*`.
- The database here is a restore of production and may lag by up to a day.
- Deploys are manual: pull, migrate, restart. There is no CI hook on this box.
