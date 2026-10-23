# Shopping UI demo

A tiny mobile-first React demo with four views: My list, Products, History, and Insights.

Pick saved sample products, check them off while shopping, and open a product to see its description, details, and nutrition. Polar Black Cherry Seltzer is a real product recognition example with an external Walmart photo and source link; the other products remain illustrative samples. Purchase history remains sample data, without any invented Polar purchases. History shows five sample shopping trips across four weeks; Buy again adds their products to the list and increases existing quantities. Insights derives weekly spending, category totals, and drink purchase volumes from the same sample history records. Refreshing resets the demo. There is no backend, persistence, or AI connection.

```sh
npm install
npm run dev
```

`npm run build` creates static production files in `dist/`.
